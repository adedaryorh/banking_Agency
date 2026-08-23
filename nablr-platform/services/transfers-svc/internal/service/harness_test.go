package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/providers"
)

// schemaRelPath is db/schema.sql seen from this package directory.
const schemaRelPath = "../../db/schema.sql"

// allTables is every table the schema owns, ordered so a single TRUNCATE ...
// CASCADE resets state between tests without tripping over foreign keys.
const truncateAll = `TRUNCATE
  provider_webhooks, provider_requests, outbox_events,
  scheduled_payment_runs, scheduled_payments,
  transfer_events, transfer_quotes, transfers,
  holds, ledger_entries, ledger_transactions,
  beneficiaries, customer_limits, wallets
  RESTART IDENTITY CASCADE`

type harness struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	svc  *Service
	mock *providers.MockProvider
}

const testPIN = "1234"

type fakeIdentity struct {
	tier       int32
	status     string
	sanctioned bool
	pep        bool
	pinOK      bool
	pinErr     error
	userErr    error
	kycErr     error
}

// permissiveIdentity is the fake configured to let any authorized transfer
// through: an active Tier 3 customer with a valid PIN and no compliance flags.
func permissiveIdentity() *fakeIdentity {
	return &fakeIdentity{tier: 3, status: "active", pinOK: true}
}

func (f *fakeIdentity) VerifyPIN(context.Context, uuid.UUID, string) (bool, error) {
	return f.pinOK, f.pinErr
}

func (f *fakeIdentity) GetUser(_ context.Context, u uuid.UUID) (UserProfile, error) {
	return UserProfile{UserID: u.String(), Status: f.status}, f.userErr
}

func (f *fakeIdentity) GetKYCProfile(_ context.Context, u uuid.UUID) (KYCProfile, error) {
	return KYCProfile{UserID: u.String(), Tier: f.tier, Sanctioned: f.sanctioned, PEP: f.pep}, f.kycErr
}

func (f *fakeIdentity) GetUserByUsername(_ context.Context, _ string) (UserProfile, error) {
	return UserProfile{Status: f.status}, ErrIdentityUserNotFound
}

func (f *fakeIdentity) GetUserByAccountNumber(_ context.Context, _ string) (UserProfile, error) {
	return UserProfile{Status: f.status}, ErrIdentityUserNotFound
}

// newHarness connects to TRANSFERS_TEST_DSN, ensures the schema is present, and
// resets all tables. settleDelay controls how long the mock waits before it
// would asynchronously settle an accepted payout; tests that inspect an
// in-flight payout pass a long delay so it does not settle underneath them.
func newHarness(t *testing.T, settleDelay time.Duration) *harness {
	t.Helper()
	dsn := os.Getenv("TRANSFERS_TEST_DSN")
	if dsn == "" {
		t.Skip("set TRANSFERS_TEST_DSN to a disposable database to run integration tests")
	}
	ctx := context.Background()

	// Load the schema on a one-off connection in the simple protocol: a
	// multi-statement DDL file cannot go through the extended protocol the pool
	// uses for parameterised queries. CREATE ... IF NOT EXISTS makes this
	// idempotent, so every test can call it cheaply.
	loadSchema(t, ctx, dsn)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}
	if _, err := pool.Exec(ctx, truncateAll); err != nil {
		pool.Close()
		t.Fatalf("truncate: %v", err)
	}

	mock := providers.NewMockProviderWithSink(nil, settleDelay)
	// A permissive identity fake stands in for identity-svc so these tests exercise
	// the ledger, not the control plane. Without it every Transfer would fail closed
	// on a nil identity client — which is correct in production and proved in
	// authz_test.go, but is not what these money-mechanics tests are pinning.
	svc := NewWithProvider(pool, mock).WithClients(permissiveIdentity(), nil)
	h := &harness{t: t, ctx: ctx, pool: pool, svc: svc, mock: mock}
	t.Cleanup(pool.Close)
	return h
}

func loadSchema(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	ddl, err := os.ReadFile(schemaRelPath)
	if err != nil {
		t.Fatalf("read schema %s: %v", schemaRelPath, err)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect for schema load: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Fixtures — everything a transfer needs to be legal, set directly rather than
// through the (not-yet-built) control plane.
// ---------------------------------------------------------------------------

// fundWallet creates a wallet and sets its available balance. There is
// deliberately no service method that sets a balance from nothing — money enters
// a wallet only through the ledger — so tests reach past the engine to seed it.
func (h *harness) fundWallet(user uuid.UUID, cur string, minor int64) uuid.UUID {
	h.t.Helper()
	w, err := h.svc.CreateWallet(h.ctx, user, cur)
	if err != nil {
		h.t.Fatalf("create wallet: %v", err)
	}
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE wallets SET available_minor=$2 WHERE id=$1`, w.ID, minor); err != nil {
		h.t.Fatalf("fund wallet: %v", err)
	}
	return w.ID
}

// setLimits gives a user explicit, generous ceilings so limit enforcement does
// not interfere with a test that is about something else. Each ceiling is written
// as a source='override' row for two reasons: an override survives the tier
// materialisation the transfer path runs (so the seeded value is what the test
// actually exercises, not a tier default that silently replaced it), and it is
// the shape a real compliance-set limit takes.
func (h *harness) setLimits(user uuid.UUID, cur string, perTx, daily, cap int64) {
	h.t.Helper()
	h.setLimit(user, cur, limitPerTransaction, perTx)
	h.setLimit(user, cur, limitDailyOutbound, daily)
	h.setLimit(user, cur, limitBalanceCap, cap)
}

// setLimit upserts one active versioned ceiling. The ON CONFLICT target names the
// partial unique index's predicate (effective_to IS NULL) so a repeated call
// updates the in-force row rather than stacking a second active one.
func (h *harness) setLimit(user uuid.UUID, cur, limitType string, amount int64) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx,
		`INSERT INTO customer_limits (user_id, limit_type, currency, amount_minor, source, reason)
		 VALUES ($1, $2, $3, $4, 'override', 'integration test fixture')
		 ON CONFLICT (user_id, limit_type, currency) WHERE effective_to IS NULL
		 DO UPDATE SET amount_minor = EXCLUDED.amount_minor`,
		user, limitType, cur, amount); err != nil {
		h.t.Fatalf("set limit %s: %v", limitType, err)
	}
}

// setCountLimit upserts one active versioned count ceiling (daily_count), which
// lives in the count_limit column rather than amount_minor. Same override shape
// and ON CONFLICT target as setLimit.
func (h *harness) setCountLimit(user uuid.UUID, cur, limitType string, count int32) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx,
		`INSERT INTO customer_limits (user_id, limit_type, currency, count_limit, source, reason)
		 VALUES ($1, $2, $3, $4, 'override', 'integration test fixture')
		 ON CONFLICT (user_id, limit_type, currency) WHERE effective_to IS NULL
		 DO UPDATE SET count_limit = EXCLUDED.count_limit`,
		user, limitType, cur, count); err != nil {
		h.t.Fatalf("set count limit %s: %v", limitType, err)
	}
}

// bankPayee adds a bank beneficiary and ages it out of the cooling period, so a
// test can send any amount without tripping the new-payee cap. account is the
// plaintext number a test can hand to mock.InjectError to script a rail error.
func (h *harness) bankPayee(owner uuid.UUID, account, bankCode string) uuid.UUID {
	h.t.Helper()
	b, err := h.svc.AddBeneficiary(h.ctx, owner, BeneficiaryInput{
		Type: "bank", BankCode: bankCode, AccountNumber: account,
		AccountName: "Test Payee", Currency: "NGN",
	})
	if err != nil {
		h.t.Fatalf("add bank payee: %v", err)
	}
	h.agePayee(b.ID)
	return b.ID
}

// internalPayee adds an internal (wallet-to-wallet) beneficiary pointing at
// recipient, aged out of cooling.
func (h *harness) internalPayee(owner, recipient uuid.UUID) uuid.UUID {
	h.t.Helper()
	b, err := h.svc.AddBeneficiary(h.ctx, owner, BeneficiaryInput{
		Type: "internal", RecipientUserID: &recipient,
		AccountName: "Internal Payee", Currency: "NGN",
	})
	if err != nil {
		h.t.Fatalf("add internal payee: %v", err)
	}
	h.agePayee(b.ID)
	return b.ID
}

func (h *harness) agePayee(id uuid.UUID) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE beneficiaries SET cooling_period_ends_at=NULL WHERE id=$1`, id); err != nil {
		h.t.Fatalf("age payee: %v", err)
	}
}

// backdate makes a transfer look old enough for the release sweep to consider
// it, without waiting. The sweep's age window is measured against created_at.
func (h *harness) backdate(id uuid.UUID, d time.Duration) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE transfers SET created_at = now() - $2::interval WHERE id=$1`,
		id, d.String()); err != nil {
		h.t.Fatalf("backdate transfer: %v", err)
	}
}

func (h *harness) balance(walletID uuid.UUID) (available, reserved int64) {
	h.t.Helper()
	if err := h.pool.QueryRow(h.ctx,
		`SELECT available_minor, reserved_minor FROM wallets WHERE id=$1`, walletID).
		Scan(&available, &reserved); err != nil {
		h.t.Fatalf("read balance: %v", err)
	}
	return available, reserved
}

func (h *harness) transfer(id uuid.UUID) db.Transfer {
	h.t.Helper()
	tr, err := h.svc.q.TransferInternalByID(h.ctx, id)
	if err != nil {
		h.t.Fatalf("read transfer: %v", err)
	}
	return tr
}

// ledgerSums returns the total debits and total credits across every ledger
// transaction. Double-entry means these must be equal, always.
func (h *harness) ledgerSums() (debits, credits int64) {
	h.t.Helper()
	if err := h.pool.QueryRow(h.ctx, `
		SELECT
		  coalesce(sum(amount_minor) FILTER (WHERE entry_type='debit'), 0),
		  coalesce(sum(amount_minor) FILTER (WHERE entry_type='credit'), 0)
		FROM ledger_entries`).Scan(&debits, &credits); err != nil {
		h.t.Fatalf("ledger sums: %v", err)
	}
	return debits, credits
}

func (h *harness) countRows(table string) int64 {
	h.t.Helper()
	var n int64
	// table is a compile-time constant from the caller, never user input.
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		h.t.Fatalf("count %s: %v", table, err)
	}
	return n
}
