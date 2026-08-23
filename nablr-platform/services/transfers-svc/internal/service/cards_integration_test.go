package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "nabla/transfers-svc/internal/platform/db"
	"nabla/transfers-svc/internal/providers"
)

type cardHarness struct {
	t     *testing.T
	ctx   context.Context
	pool  *pgxpool.Pool
	cards *CardsService
	mock  *providers.MockCardIssuer

	customerID uuid.UUID
	walletID   uuid.UUID
	accountID  uuid.UUID
}

const truncateCards = `TRUNCATE
  round_up_items, round_up_accruals,
  spending_control_events, spending_control_overrides, spending_controls,
  card_delivery_requests, card_events, card_transactions, card_authorisations,
  card_controls, card_cardholders, cards,
  wallet_transactions, compliance_cases,
  holds, ledger_entries, ledger_transactions, ledger_accounts,
  customer_limits, wallets
  RESTART IDENTITY CASCADE`

func newCardHarness(t *testing.T, openingBalanceMinor int64) *cardHarness {
	t.Helper()
	dsn := os.Getenv("TRANSFERS_TEST_DSN")
	if dsn == "" {
		t.Skip("TRANSFERS_TEST_DSN not set; skipping card integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	// A missing cards schema is said out loud. A test that silently passed
	// because the tables were not there would be worse than no test.
	var present bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.cards') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatalf("schema probe: %v", err)
	}
	if !present {
		t.Fatal("the cards schema is not applied; run migrations 003 and 004 against TRANSFERS_TEST_DSN")
	}
	if _, err := pool.Exec(ctx, truncateCards); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	raw := platformdb.NewAdapter(pool)
	clk := fixedClock{at: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	_ = clk

	ledger := NewLedgerService(raw, clk)
	wallets := NewWalletService(raw, clk)
	controls := NewControlsService(raw, clk)
	limits := NewLimitsService(raw)
	mock := providers.NewMockCardIssuer()

	h := &cardHarness{
		t: t, ctx: ctx, pool: pool, mock: mock,
		cards:      NewCardsService(raw, mock, ledger, wallets, controls, limits, clk),
		customerID: uuid.New(),
	}

	// A funded ledger account and the wallet that points at it.
	h.accountID = uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO ledger_accounts
			(id, account_type, currency, owner_type, customer_id, name, code,
			 balance_minor, reserved_minor, pending_minor, status)
		VALUES ($1, 'customer_asset', 'NGN', 'customer', $2, 'test wallet', $3,
		        $4, 0, 0, 'active')`,
		h.accountID, h.customerID, "customer:"+h.customerID.String(),
		openingBalanceMinor); err != nil {
		t.Fatalf("seed ledger account: %v", err)
	}

	h.walletID = uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO wallets (id, user_id, customer_id, account_id, ledger_account_id,
		                     currency, name, wallet_type, is_default, status,
		                     available_minor, reserved_minor)
		VALUES ($1, $2, $2, $3, $3, 'NGN', 'Main', 'spending', true, 'active', $4, 0)`,
		h.walletID, h.customerID, h.accountID, openingBalanceMinor); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}

	// Verification ceilings, without which every authorisation is refused as a
	// compliance restriction — which is itself asserted below.
	h.setLimits(50_000_00, 200_000_00)
	return h
}

func (h *cardHarness) setLimits(perTxn, daily int64) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx, `
		DELETE FROM customer_limits WHERE user_id = $1`, h.customerID); err != nil {
		h.t.Fatalf("clear limits: %v", err)
	}
	for typ, amount := range map[string]int64{"per_transaction": perTxn, "daily_outbound": daily} {
		if _, err := h.pool.Exec(h.ctx, `
			INSERT INTO customer_limits (user_id, limit_type, currency, amount_minor, source)
			VALUES ($1, $2, 'NGN', $3, 'tier')`, h.customerID, typ, amount); err != nil {
			h.t.Fatalf("seed limit %s: %v", typ, err)
		}
	}
}

func (h *cardHarness) available() int64 {
	h.t.Helper()
	var v int64
	if err := h.pool.QueryRow(h.ctx,
		`SELECT available_minor FROM ledger_accounts WHERE id = $1`, h.accountID).Scan(&v); err != nil {
		h.t.Fatalf("read balance: %v", err)
	}
	return v
}

func (h *cardHarness) balance() int64 {
	h.t.Helper()
	var v int64
	if err := h.pool.QueryRow(h.ctx,
		`SELECT balance_minor FROM ledger_accounts WHERE id = $1`, h.accountID).Scan(&v); err != nil {
		h.t.Fatalf("read balance: %v", err)
	}
	return v
}

func (h *cardHarness) issueVirtual() *Card {
	h.t.Helper()
	card, err := h.cards.Issue(h.ctx, IssueInput{
		CustomerID: h.customerID, WalletID: h.walletID, CardType: "virtual",
	})
	if err != nil {
		h.t.Fatalf("issue: %v", err)
	}
	return card
}

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

// ---------------------------------------------------------------------------

// Issuing charges the fee and the card exists — one transaction, so neither
// can happen without the other.
func TestCardsIssueChargesTheFee(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	before := h.balance()

	card := h.issueVirtual()
	if card.Status != "active" {
		t.Errorf("a new card is %q, want active", card.Status)
	}
	if card.Currency != "NGN" {
		t.Errorf("card currency = %q", card.Currency)
	}
	if want := before - CardFeeMinor("virtual"); h.balance() != want {
		t.Errorf("balance after issue = %d, want %d (fee %d)", h.balance(), want, CardFeeMinor("virtual"))
	}

	// Default controls exist and international is OFF until opted into.
	var online, international bool
	if err := h.pool.QueryRow(h.ctx,
		`SELECT allow_online, allow_international FROM card_controls WHERE card_id = $1`,
		card.ID).Scan(&online, &international); err != nil {
		t.Fatalf("controls not created: %v", err)
	}
	if !online || international {
		t.Errorf("default controls: online=%t international=%t; want true/false", online, international)
	}
}

// One virtual and one physical is the whole allowance.
func TestCardsOnePerType(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	h.issueVirtual()

	_, err := h.cards.Issue(h.ctx, IssueInput{
		CustomerID: h.customerID, WalletID: h.walletID, CardType: "virtual",
	})
	if !errors.Is(err, ErrCardLimit) {
		t.Fatalf("second virtual card: %v, want ErrCardLimit", err)
	}

	// A physical card is a different allowance — and needs somewhere to go.
	_, err = h.cards.Issue(h.ctx, IssueInput{
		CustomerID: h.customerID, WalletID: h.walletID, CardType: "physical",
	})
	if !errors.Is(err, ErrDeliveryRequired) {
		t.Fatalf("physical card with no address: %v, want ErrDeliveryRequired", err)
	}
}

// Approving reserves the money; it does not spend it.
func TestCardsAuthorizeHoldsFunds(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()
	availableAfterFee := h.available()

	d, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "auth_1", CardID: card.ID,
		AmountMinor: 1_500_00, Currency: "NGN",
		MerchantName: "Shoprite", MerchantMCC: "5411", EntryMode: "chip",
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !d.Approved {
		t.Fatalf("declined: %s", d.DeclineReason)
	}
	if want := availableAfterFee - 1_500_00; h.available() != want {
		t.Errorf("available = %d, want %d (the hold)", h.available(), want)
	}
	// A hold is not a spend: the posted balance has not moved.
	if h.balance() != 10_000_00-CardFeeMinor("virtual") {
		t.Errorf("balance moved on an authorisation: %d", h.balance())
	}
}

// The same authorisation delivered twice is one decision and one hold.
func TestCardsAuthorizeIsIdempotent(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()

	req := AuthRequest{
		ProviderAuthID: "auth_replay", CardID: card.ID,
		AmountMinor: 900_00, Currency: "NGN", MerchantName: "Jumia",
		MerchantMCC: "5411", EntryMode: "ecommerce",
	}
	first, err := h.cards.Authorize(h.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	afterFirst := h.available()

	second, err := h.cards.Authorize(h.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.AuthorisationID != first.AuthorisationID {
		t.Errorf("a redelivered authorisation made a new record: %s then %s",
			first.AuthorisationID, second.AuthorisationID)
	}
	if h.available() != afterFirst {
		t.Errorf("a redelivered authorisation reserved the money twice: %d then %d",
			afterFirst, h.available())
	}
}

// Every decline records why, and each reason is the true one.
func TestCardsDeclineReasons(t *testing.T) {
	t.Run("insufficient funds", func(t *testing.T) {
		h := newCardHarness(t, 1_000_00)
		card := h.issueVirtual()
		d, err := h.cards.Authorize(h.ctx, AuthRequest{
			ProviderAuthID: "a", CardID: card.ID, AmountMinor: 5_000_00,
			Currency: "NGN", MerchantName: "M", EntryMode: "chip",
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Approved || d.DeclineReason != "insufficient_funds" {
			t.Errorf("got approved=%t reason=%q", d.Approved, d.DeclineReason)
		}
	})

	t.Run("frozen card", func(t *testing.T) {
		h := newCardHarness(t, 10_000_00)
		card := h.issueVirtual()
		if _, err := h.cards.SetCardFrozen(h.ctx, card.ID, h.customerID, uuid.New(), true); err != nil {
			t.Fatal(err)
		}
		d, err := h.cards.Authorize(h.ctx, AuthRequest{
			ProviderAuthID: "b", CardID: card.ID, AmountMinor: 100_00,
			Currency: "NGN", MerchantName: "M", EntryMode: "chip",
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Approved || d.DeclineReason != "card_frozen" {
			t.Errorf("got approved=%t reason=%q", d.Approved, d.DeclineReason)
		}
	})

	t.Run("international blocked by default", func(t *testing.T) {
		h := newCardHarness(t, 10_000_00)
		card := h.issueVirtual()
		d, err := h.cards.Authorize(h.ctx, AuthRequest{
			ProviderAuthID: "c", CardID: card.ID, AmountMinor: 100_00,
			Currency: "NGN", MerchantName: "M", EntryMode: "ecommerce",
			IsInternational: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Approved || d.DeclineReason != "country_blocked" {
			t.Errorf("got approved=%t reason=%q", d.Approved, d.DeclineReason)
		}
	})

	t.Run("over the tier ceiling", func(t *testing.T) {
		h := newCardHarness(t, 100_000_00)
		card := h.issueVirtual()
		h.setLimits(1_000_00, 200_000_00)
		d, err := h.cards.Authorize(h.ctx, AuthRequest{
			ProviderAuthID: "d", CardID: card.ID, AmountMinor: 5_000_00,
			Currency: "NGN", MerchantName: "M", EntryMode: "chip",
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Approved || d.DeclineReason != "limit_exceeded" {
			t.Errorf("got approved=%t reason=%q", d.Approved, d.DeclineReason)
		}
	})

	// An unknown ceiling is zero, never infinity — and it is not the
	// customer's fault, so the reason says restriction rather than blaming
	// their balance.
	t.Run("no ceilings on record", func(t *testing.T) {
		h := newCardHarness(t, 100_000_00)
		card := h.issueVirtual()
		if _, err := h.pool.Exec(h.ctx,
			`DELETE FROM customer_limits WHERE user_id = $1`, h.customerID); err != nil {
			t.Fatal(err)
		}
		d, err := h.cards.Authorize(h.ctx, AuthRequest{
			ProviderAuthID: "e", CardID: card.ID, AmountMinor: 100_00,
			Currency: "NGN", MerchantName: "M", EntryMode: "chip",
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Approved || d.DeclineReason != "compliance_restriction" {
			t.Errorf("got approved=%t reason=%q", d.Approved, d.DeclineReason)
		}
	})
}

// The customer's own ethical control declines, and records which one so they
// can dispute a miscategorised merchant.
func TestCardsEthicalControlDeclines(t *testing.T) {
	h := newCardHarness(t, 100_000_00)
	card := h.issueVirtual()

	controls := NewControlsService(platformdb.NewAdapter(h.pool),
		fixedClock{at: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)})
	if _, err := controls.Enable(h.ctx, h.customerID, uuid.New(),
		"gambling", "block", false, nil); err != nil {
		t.Fatalf("enable control: %v", err)
	}

	// 7995 is betting, seeded by migration 004.
	d, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "eth", CardID: card.ID, AmountMinor: 500_00,
		Currency: "NGN", MerchantName: "BetCo", MerchantMCC: "7995", EntryMode: "ecommerce",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved || d.DeclineReason != "ethical_control_blocked" {
		t.Fatalf("got approved=%t reason=%q", d.Approved, d.DeclineReason)
	}

	var recorded int
	if err := h.pool.QueryRow(h.ctx, `
		SELECT count(*) FROM spending_control_events
		 WHERE customer_id = $1 AND outcome = 'blocked' AND merchant_mcc = '7995'`,
		h.customerID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Errorf("%d control events recorded, want 1 — a block nobody can see is one nobody can dispute", recorded)
	}

	// The same merchant with no control on it goes through.
	d, err = h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "eth2", CardID: card.ID, AmountMinor: 500_00,
		Currency: "NGN", MerchantName: "Shoprite", MerchantMCC: "5411", EntryMode: "ecommerce",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Approved {
		t.Errorf("an unrelated merchant was declined: %s", d.DeclineReason)
	}
}

// Settlement captures the hold, posts a balanced journal and writes the feed
// item — and a second advice for the same authorisation changes nothing.
func TestCardsSettleAndRefund(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()
	afterFee := h.balance()

	if _, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "auth_settle", CardID: card.ID,
		AmountMinor: 2_000_00, Currency: "NGN",
		MerchantName: "Shoprite", MerchantMCC: "5411", EntryMode: "chip",
	}); err != nil {
		t.Fatal(err)
	}

	// Partial settlement is the scheme norm: less than authorised is normal.
	if err := h.cards.SettleCard(h.ctx, "mock", "auth_settle", 1_800_00, "NGN"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if want := afterFee - 1_800_00; h.balance() != want {
		t.Errorf("balance after settlement = %d, want %d", h.balance(), want)
	}

	// The journal balances. If it does not, money was created.
	var debits, credits int64
	if err := h.pool.QueryRow(h.ctx, `
		SELECT coalesce(sum(amount_minor) FILTER (WHERE direction = 'debit'), 0),
		       coalesce(sum(amount_minor) FILTER (WHERE direction = 'credit'), 0)
		  FROM ledger_entries`).Scan(&debits, &credits); err != nil {
		t.Fatal(err)
	}
	if debits != credits {
		t.Errorf("the ledger does not balance: %d debits against %d credits", debits, credits)
	}

	var feedRows int
	if err := h.pool.QueryRow(h.ctx, `
		SELECT count(*) FROM wallet_transactions
		 WHERE customer_id = $1 AND transaction_type = 'card_payment'`,
		h.customerID).Scan(&feedRows); err != nil {
		t.Fatal(err)
	}
	if feedRows != 1 {
		t.Errorf("%d card_payment feed rows, want 1", feedRows)
	}

	// A redelivered settlement is a no-op, not a second debit.
	if err := h.cards.SettleCard(h.ctx, "mock", "auth_settle", 1_800_00, "NGN"); err != nil {
		t.Fatalf("duplicate settlement should be accepted quietly: %v", err)
	}
	if want := afterFee - 1_800_00; h.balance() != want {
		t.Errorf("a duplicate settlement debited twice: %d, want %d", h.balance(), want)
	}

	// The refund puts it back by reversing the settlement's own entries.
	if err := h.cards.RefundCard(h.ctx, "mock", "auth_settle", 1_800_00, "NGN"); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if h.balance() != afterFee {
		t.Errorf("balance after refund = %d, want %d", h.balance(), afterFee)
	}

	// And a redelivered refund does not pay twice.
	if err := h.cards.RefundCard(h.ctx, "mock", "auth_settle", 1_800_00, "NGN"); err != nil {
		t.Fatalf("duplicate refund should be accepted quietly: %v", err)
	}
	if h.balance() != afterFee {
		t.Errorf("a duplicate refund credited twice: %d, want %d", h.balance(), afterFee)
	}
}

// More than was taken is not a refund.
func TestCardsRefundCannotExceedSettlement(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()

	if _, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "auth_big", CardID: card.ID,
		AmountMinor: 1_000_00, Currency: "NGN",
		MerchantName: "M", MerchantMCC: "5411", EntryMode: "chip",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.cards.SettleCard(h.ctx, "mock", "auth_big", 1_000_00, "NGN"); err != nil {
		t.Fatal(err)
	}
	if err := h.cards.RefundCard(h.ctx, "mock", "auth_big", 2_000_00, "NGN"); err == nil {
		t.Fatal("a refund larger than the settlement was accepted; that creates money")
	}
	if err := h.cards.RefundCard(h.ctx, "mock", "auth_big", 0, "NGN"); err == nil {
		t.Fatal("a zero refund was accepted")
	}
	// A currency that is not the payment's is not a smaller refund, it is a
	// different one.
	if err := h.cards.RefundCard(h.ctx, "mock", "auth_big", 100_00, "USD"); err == nil {
		t.Fatal("a refund in another currency was accepted")
	}
}

// A settlement must be positive and no larger than what was authorised. This is
// the one place a card could quietly spend money nobody reserved.
func TestCardsSettleCannotExceedAuthorisation(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()
	afterFee := h.balance()

	if _, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "auth_bounds", CardID: card.ID,
		AmountMinor: 1_000_00, Currency: "NGN",
		MerchantName: "M", MerchantMCC: "5411", EntryMode: "chip",
	}); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []struct {
		name     string
		minor    int64
		currency string
	}{
		{"zero", 0, "NGN"},
		{"negative", -100, "NGN"},
		{"more than authorised", 1_000_01, "NGN"},
		{"another currency", 500_00, "USD"},
	} {
		if err := h.cards.SettleCard(h.ctx, "mock", "auth_bounds", bad.minor, bad.currency); err == nil {
			t.Errorf("a %s settlement was accepted", bad.name)
		}
	}
	if h.balance() != afterFee {
		t.Errorf("a refused settlement moved money: balance %d, want %d", h.balance(), afterFee)
	}

	// An authorisation nobody holds cannot be settled.
	if err := h.cards.SettleCard(h.ctx, "mock", "never_seen", 100, "NGN"); !errors.Is(err, ErrAuthNotFound) {
		t.Errorf("settling an unknown authorisation: %v, want ErrAuthNotFound", err)
	}
	// Nor can one from a different rail: the provider is half the key.
	if err := h.cards.SettleCard(h.ctx, "sudo", "auth_bounds", 100, "NGN"); !errors.Is(err, ErrAuthNotFound) {
		t.Errorf("settling under the wrong provider: %v, want ErrAuthNotFound", err)
	}
}

// Freezing tells the issuer, records the event, and the card can be unfrozen.
// Terminating is final and idempotent.
func TestCardsFreezeAndTerminate(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()

	var providerCardID string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT provider_card_id FROM cards WHERE id = $1`, card.ID).Scan(&providerCardID); err != nil {
		t.Fatal(err)
	}

	frozen, err := h.cards.SetCardFrozen(h.ctx, card.ID, h.customerID, uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Status != "frozen" {
		t.Errorf("status = %q, want frozen", frozen.Status)
	}
	if got := h.mock.StateOf(providerCardID); got != "frozen" {
		t.Errorf("the issuer was not told; it thinks the card is %q", got)
	}

	thawed, err := h.cards.SetCardFrozen(h.ctx, card.ID, h.customerID, uuid.New(), false)
	if err != nil {
		t.Fatal(err)
	}
	if thawed.Status != "active" {
		t.Errorf("status = %q, want active", thawed.Status)
	}

	if err := h.cards.TerminateCard(h.ctx, card.ID, h.customerID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	// Deleting twice is not an error.
	if err := h.cards.TerminateCard(h.ctx, card.ID, h.customerID, uuid.New()); err != nil {
		t.Fatalf("terminating twice should be quiet: %v", err)
	}
	list, err := h.cards.Cards(h.ctx, h.customerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("a terminated card is still listed: %+v", list)
	}
	// A frozen card cannot be re-frozen once terminated.
	if _, err := h.cards.SetCardFrozen(h.ctx, card.ID, h.customerID, uuid.New(), true); !errors.Is(err, ErrCardNotActive) {
		t.Errorf("freezing a terminated card: %v, want ErrCardNotActive", err)
	}
}

// A card issued in a currency it cannot be priced or settled in is refused
// BEFORE the issuer is asked, so nobody's card is created and then rejected.
func TestCardsRefusesNonNairaWallet(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE wallets SET currency = 'USD' WHERE id = $1`, h.walletID); err != nil {
		t.Fatal(err)
	}
	_, err := h.cards.Issue(h.ctx, IssueInput{
		CustomerID: h.customerID, WalletID: h.walletID, CardType: "virtual",
	})
	if !errors.Is(err, ErrCurrencyUnsupported) {
		t.Fatalf("issue against a USD wallet: %v, want ErrCurrencyUnsupported", err)
	}
	var cards int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM cards`).Scan(&cards); err != nil {
		t.Fatal(err)
	}
	if cards != 0 {
		t.Errorf("%d cards were written for a refused issue", cards)
	}
}

// A physical card is LIVE from the moment it is issued; the delivery state
// says where the plastic is, not whether the card works.
func TestCardsPhysicalIsLiveAndTracked(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card, err := h.cards.Issue(h.ctx, IssueInput{
		CustomerID: h.customerID, WalletID: h.walletID, CardType: "physical",
		Delivery: &Delivery{Line1: "9 Courier Rd", City: "Lagos", State: "Lagos", Phone: "+2348000000000"},
	})
	if err != nil {
		t.Fatalf("issue physical: %v", err)
	}
	if card.Status != "active" {
		t.Errorf("a physical card is %q on issue, want active", card.Status)
	}
	if card.DeliveryStatus != "requested" {
		t.Errorf("delivery status = %q, want requested", card.DeliveryStatus)
	}
	if card.DeliveryTracking == "" || card.DeliveryEstimated == nil {
		t.Errorf("no tracking (%q) or estimate (%v)", card.DeliveryTracking, card.DeliveryEstimated)
	}
	if got := h.cards.CardDeliveryStatus(h.ctx, h.customerID, card.ID); got != "requested" {
		t.Errorf("CardDeliveryStatus = %q, want requested", got)
	}

	// One open delivery per card.
	if _, err := h.cards.RequestCardDelivery(h.ctx, h.customerID, card.ID,
		"1 Other St", "Abuja", "FCT", "+2348111111111"); !errors.Is(err, ErrDeliveryOpen) {
		t.Errorf("second delivery request: %v, want ErrDeliveryOpen", err)
	}

	if _, err := h.cards.RequestCardDelivery(h.ctx, h.customerID, uuid.New(),
		"1 Other St", "Abuja", "FCT", "+234"); !errors.Is(err, ErrCardNotFound) {
		t.Errorf("delivery for an unknown card: %v, want ErrCardNotFound", err)
	}

	// The fee charged is the physical one.
	if want := 10_000_00 - CardFeeMinor("physical"); h.balance() != want {
		t.Errorf("balance = %d, want %d after the physical card fee", h.balance(), want)
	}
}

// The issuer can close a card without us asking, and a card it has stopped
// must not still look usable.
func TestCardsCloseByProviderCard(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()

	var providerCardID string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT provider_card_id FROM cards WHERE id = $1`, card.ID).Scan(&providerCardID); err != nil {
		t.Fatal(err)
	}
	if err := h.cards.CloseByProviderCard(h.ctx, providerCardID, "issuer_terminated"); err != nil {
		t.Fatal(err)
	}
	after, err := h.cards.CardByID(h.ctx, card.ID, h.customerID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "terminated" {
		t.Errorf("status = %q, want terminated", after.Status)
	}
	// Hearing it twice, or about a card we do not hold, is fine.
	if err := h.cards.CloseByProviderCard(h.ctx, providerCardID, "issuer_terminated"); err != nil {
		t.Errorf("a redelivered termination errored: %v", err)
	}
	if err := h.cards.CloseByProviderCard(h.ctx, "unknown-card", "issuer_terminated"); err != nil {
		t.Errorf("a termination for an unknown card errored: %v", err)
	}
}

// Round-ups accrue on card spend; the sweep moves the money later.
func TestCardsRoundUpAccrual(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()

	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO round_up_accruals (customer_id, currency, round_up_multiplier, is_active)
		VALUES ($1, 'NGN', 2, true)`, h.customerID); err != nil {
		t.Fatal(err)
	}

	if _, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "auth_round", CardID: card.ID,
		AmountMinor: 1_234_50, Currency: "NGN",
		MerchantName: "Shoprite", MerchantMCC: "5411", EntryMode: "chip",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.cards.SettleCard(h.ctx, "mock", "auth_round", 1_234_50, "NGN"); err != nil {
		t.Fatal(err)
	}

	var accrued int64
	if err := h.pool.QueryRow(h.ctx,
		`SELECT accrued_minor FROM round_up_accruals WHERE customer_id = $1`,
		h.customerID).Scan(&accrued); err != nil {
		t.Fatal(err)
	}
	// ₦1,234.50 rounds up to ₦1,235.00 — 50 kobo — times the multiplier of 2.
	if accrued != 100 {
		t.Errorf("accrued %d kobo, want 100 (50 spare change × 2)", accrued)
	}
}

// Reveal hands back the issuer's numbers and writes nothing down.
func TestCardsReveal(t *testing.T) {
	h := newCardHarness(t, 10_000_00)
	card := h.issueVirtual()

	secrets, err := h.cards.Reveal(h.ctx, card.ID, h.customerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets.PAN) != 16 || secrets.CVV == "" {
		t.Errorf("reveal returned PAN %q CVV %q", secrets.PAN, secrets.CVV)
	}
	if got := secrets.PAN[12:]; got != card.Last4 {
		t.Errorf("revealed PAN ends %q, stored last4 is %q", got, card.Last4)
	}

	// Somebody else's card is not found, not forbidden: the ownership check is
	// in the query.
	if _, err := h.cards.Reveal(h.ctx, card.ID, uuid.New()); !errors.Is(err, ErrCardNotFound) {
		t.Errorf("reveal for another customer: %v, want ErrCardNotFound", err)
	}

	// Nothing was persisted.
	var stored int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM cards WHERE provider_token = $1`, secrets.PAN).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Error("a PAN was written to the cards table")
	}
}

// Card spend counts against the SAME daily ceiling every other rail answers to.
func TestCardsSpendCountsAgainstTheDailyCeiling(t *testing.T) {
	h := newCardHarness(t, 100_000_00)
	card := h.issueVirtual()
	h.setLimits(50_000_00, 3_000_00)

	if _, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "day_1", CardID: card.ID, AmountMinor: 2_500_00,
		Currency: "NGN", MerchantName: "M", MerchantMCC: "5411", EntryMode: "chip",
	}); err != nil {
		t.Fatal(err)
	}

	// The approved-but-unsettled authorisation is already spending: between
	// approval and capture it appears nowhere else, and a ceiling that could
	// not see it would let the same money go twice.
	d, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "day_2", CardID: card.ID, AmountMinor: 1_000_00,
		Currency: "NGN", MerchantName: "M", MerchantMCC: "5411", EntryMode: "chip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved || d.DeclineReason != "limit_exceeded" {
		t.Errorf("second payment approved=%t reason=%q; want limit_exceeded", d.Approved, d.DeclineReason)
	}

	var used int64
	if err := h.pool.QueryRow(h.ctx, outboundQuery, h.customerID, "NGN").Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 2_500_00 {
		t.Errorf("today's outbound = %d, want 250000 (the pending card authorisation)", used)
	}
}

// adapterFor lets the funding harness reuse this one's pool and seeded wallet.
func adapterFor(h *cardHarness) DB { return platformdb.NewAdapter(h.pool) }
