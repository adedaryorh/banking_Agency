package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"nabla/transfers-svc/internal/providers"
)

// fakeCollector is a collections rail with no network.
type fakeCollector struct {
	mu       sync.Mutex
	sandbox  bool
	bankName string
	accounts map[string]*providers.VirtualAccount // by reference
	byNumber map[string]*providers.VirtualAccount
	payments map[string]*providers.Collection
	created  int
	// failCreate makes provisioning fail, to prove a customer is not left with
	// a half-written account row.
	failCreate error
}

func newFakeCollector() *fakeCollector {
	return &fakeCollector{
		bankName: "Providus Bank",
		accounts: map[string]*providers.VirtualAccount{},
		byNumber: map[string]*providers.VirtualAccount{},
		payments: map[string]*providers.Collection{},
	}
}

func (f *fakeCollector) Info() providers.ProviderInfo {
	return providers.ProviderInfo{Name: "novac", Version: "test", Sandbox: f.sandbox}
}

func (f *fakeCollector) CreateVirtualAccount(ctx context.Context, req providers.VirtualAccountRequest) (*providers.VirtualAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate != nil {
		return nil, f.failCreate
	}
	// Idempotent on reference, exactly as the real one is.
	if a, ok := f.accounts[req.Reference]; ok {
		return a, nil
	}
	f.created++
	a := &providers.VirtualAccount{
		ProviderRef:   req.Reference,
		AccountNumber: "90000000" + string(rune('0'+f.created/10%10)) + string(rune('0'+f.created%10)),
		AccountName:   req.AccountName,
		BankName:      f.bankName,
		BankCode:      "101",
		Permanent:     true,
	}
	f.accounts[req.Reference] = a
	f.byNumber[a.AccountNumber] = a
	return a, nil
}

func (f *fakeCollector) GetVirtualAccount(ctx context.Context, number string) (*providers.VirtualAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.byNumber[number]; ok {
		return a, nil
	}
	return nil, &providers.Error{Code: providers.ErrNotFound, Message: "no such account"}
}

func (f *fakeCollector) GetCollection(ctx context.Context, ref string) (*providers.Collection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.payments[ref]; ok {
		return c, nil
	}
	return nil, &providers.Error{Code: providers.ErrNotFound, Message: "no such transaction"}
}

// pay is the money actually arriving at the rail, which the webhook only hints at.
func (f *fakeCollector) pay(ref, accountNumber string, minor int64, status providers.CollectionStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payments[ref] = &providers.Collection{
		ProviderRef: ref, AccountNumber: accountNumber,
		AmountMinor: minor, FeeMinor: 0, Currency: "NGN",
		Status: status, SenderName: "ADEBAYO O", Narrative: "rent",
		OccurredAt: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
	}
}

type fundingHarness struct {
	*cardHarness
	funding *FundingService
	rail    *fakeCollector
}

func newFundingHarness(t *testing.T, opening int64) *fundingHarness {
	t.Helper()
	h := newCardHarness(t, opening)
	if _, err := h.pool.Exec(h.ctx, `TRUNCATE collections, funding_accounts CASCADE`); err != nil {
		t.Fatalf("truncate funding: %v", err)
	}
	rail := newFakeCollector()
	clk := fixedClock{at: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	raw := adapterFor(h)
	return &fundingHarness{
		cardHarness: h,
		rail:        rail,
		funding: NewFundingService(raw, rail,
			NewLedgerService(raw, clk), NewWalletService(raw, clk),
			NewLimitsService(raw), clk),
	}
}

// ---------------------------------------------------------------------------

// Provisioning is idempotent: asking twice gives one account, not two.
//
// A second number would split the customer's incoming money across two
// accounts, only one of which the app shows them.
func TestFunding_EnsureAccountIsIdempotent(t *testing.T) {
	h := newFundingHarness(t, 0)

	first, err := h.funding.EnsureAccount(h.ctx, h.customerID)
	if err != nil {
		t.Fatal(err)
	}
	if first.AccountNumber == "" {
		t.Fatal("no account number")
	}
	second, err := h.funding.EnsureAccount(h.ctx, h.customerID)
	if err != nil {
		t.Fatal(err)
	}
	if first.AccountNumber != second.AccountNumber {
		t.Errorf("two calls gave two numbers: %s then %s",
			first.AccountNumber, second.AccountNumber)
	}
	if h.rail.created != 1 {
		t.Errorf("the rail was asked to create %d accounts, want 1", h.rail.created)
	}

	var rows int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM funding_accounts WHERE customer_id = $1`, h.customerID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("%d funding_accounts rows, want 1", rows)
	}
}

// A customer with no wallet gets a clear answer, not a provider call.
func TestFunding_NoWallet(t *testing.T) {
	h := newFundingHarness(t, 0)
	if _, err := h.funding.EnsureAccount(h.ctx, uuid.New()); !errors.Is(err, ErrNoWallet) {
		t.Fatalf("EnsureAccount for a walletless customer: %v, want ErrNoWallet", err)
	}
	if h.rail.created != 0 {
		t.Error("the provider was called for a customer with nowhere to put the money")
	}
}

// A provider refusal must not leave a half-written account behind.
func TestFunding_ProvisionFailureWritesNothing(t *testing.T) {
	h := newFundingHarness(t, 0)
	h.rail.failCreate = &providers.Error{
		Code: providers.ErrRejected, Message: "issuer unavailable"}

	if _, err := h.funding.EnsureAccount(h.ctx, h.customerID); err == nil {
		t.Fatal("provisioning succeeded despite the provider refusing")
	}
	var rows int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM funding_accounts`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d funding_accounts rows were written for a failed provision", rows)
	}
}

// Money in: the wallet balance goes up, the journal balances, and the customer
// can see what happened.
func TestFunding_RecordCreditsTheWallet(t *testing.T) {
	h := newFundingHarness(t, 0)
	acct, err := h.funding.EnsureAccount(h.ctx, h.customerID)
	if err != nil {
		t.Fatal(err)
	}
	if h.available() != 0 {
		t.Fatalf("the wallet started with %d, expected empty", h.available())
	}

	h.rail.pay("txn_1", acct.AccountNumber, 25_000_00, providers.CollectionSettled)
	if err := h.funding.Record(h.ctx, "txn_1"); err != nil {
		t.Fatalf("record: %v", err)
	}

	if h.available() != 25_000_00 {
		t.Errorf("available = %d, want 2500000", h.available())
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

	// And the customer can see it: a balance that changes with nothing to
	// explain it is the most alarming thing a banking app can show.
	var feedRows int
	var balanceAfter int64
	var counterparty string
	if err := h.pool.QueryRow(h.ctx, `
		SELECT count(*), coalesce(max(balance_after_minor), 0), coalesce(max(counterparty_name), '')
		  FROM wallet_transactions
		 WHERE customer_id = $1 AND transaction_type = 'deposit'`,
		h.customerID).Scan(&feedRows, &balanceAfter, &counterparty); err != nil {
		t.Fatal(err)
	}
	if feedRows != 1 {
		t.Errorf("%d deposit feed rows, want 1", feedRows)
	}
	if balanceAfter != 25_000_00 {
		t.Errorf("the receipt says the balance after was %d, want 2500000", balanceAfter)
	}
	if counterparty != "ADEBAYO O" {
		t.Errorf("counterparty = %q, want the sender's name", counterparty)
	}

	// And they are told.
	var notified int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM outbox_events WHERE event_type = 'notification.money_in'`).
		Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified != 1 {
		t.Errorf("%d money_in notifications, want 1", notified)
	}
}

// The whole point of the unique index: a webhook delivered twice credits once.
func TestFunding_RedeliveredWebhookCreditsOnce(t *testing.T) {
	h := newFundingHarness(t, 0)
	acct, err := h.funding.EnsureAccount(h.ctx, h.customerID)
	if err != nil {
		t.Fatal(err)
	}
	h.rail.pay("txn_dup", acct.AccountNumber, 10_000_00, providers.CollectionSettled)

	if err := h.funding.Record(h.ctx, "txn_dup"); err != nil {
		t.Fatal(err)
	}
	after := h.available()

	if err := h.funding.Record(h.ctx, "txn_dup"); !errors.Is(err, ErrAlreadyRecorded) {
		t.Fatalf("second delivery: %v, want ErrAlreadyRecorded", err)
	}
	if h.available() != after {
		t.Errorf("a redelivered webhook credited twice: %d then %d", after, h.available())
	}

	var rows int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM collections`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("%d collections rows, want 1", rows)
	}
}

// Concurrent deliveries of the same payment: exactly one credit survives.
func TestFunding_ConcurrentDeliveriesCreditOnce(t *testing.T) {
	h := newFundingHarness(t, 0)
	acct, err := h.funding.EnsureAccount(h.ctx, h.customerID)
	if err != nil {
		t.Fatal(err)
	}
	h.rail.pay("txn_race", acct.AccountNumber, 7_000_00, providers.CollectionSettled)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = h.funding.Record(context.Background(), "txn_race")
		}(i)
	}
	wg.Wait()

	if h.available() != 7_000_00 {
		t.Errorf("available = %d after four concurrent deliveries, want 700000", h.available())
	}
	var rows int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM collections`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("%d collections rows after a race, want 1", rows)
	}
}

// A payment the provider has not settled is not money.
func TestFunding_UnsettledIsNotCredited(t *testing.T) {
	h := newFundingHarness(t, 0)
	acct, err := h.funding.EnsureAccount(h.ctx, h.customerID)
	if err != nil {
		t.Fatal(err)
	}

	h.rail.pay("txn_pending", acct.AccountNumber, 5_000_00, providers.CollectionPending)
	if err := h.funding.Record(h.ctx, "txn_pending"); !errors.Is(err, ErrNotSettled) {
		t.Fatalf("pending collection: %v, want ErrNotSettled", err)
	}

	h.rail.pay("txn_failed", acct.AccountNumber, 5_000_00, providers.CollectionFailed)
	if err := h.funding.Record(h.ctx, "txn_failed"); !errors.Is(err, ErrNotSettled) {
		t.Fatalf("failed collection: %v, want ErrNotSettled", err)
	}

	if h.available() != 0 {
		t.Errorf("available = %d; nothing settled, so nothing should have been credited", h.available())
	}
}

// The webhook body is not trusted: a reference the provider does not recognise
// credits nobody, however confident the caller was.
func TestFunding_UnknownReferenceCreditsNobody(t *testing.T) {
	h := newFundingHarness(t, 0)
	if _, err := h.funding.EnsureAccount(h.ctx, h.customerID); err != nil {
		t.Fatal(err)
	}
	if err := h.funding.Record(h.ctx, "a-reference-nobody-issued"); err == nil {
		t.Fatal("an unverifiable reference was recorded")
	}
	if h.available() != 0 {
		t.Error("money was credited for a payment the provider knows nothing about")
	}
}

// Money that lands on a number we do not hold is refused loudly rather than
// credited to someone.
func TestFunding_UnknownAccount(t *testing.T) {
	h := newFundingHarness(t, 0)
	if _, err := h.funding.EnsureAccount(h.ctx, h.customerID); err != nil {
		t.Fatal(err)
	}
	h.rail.pay("txn_orphan", "9999999999", 1_000_00, providers.CollectionSettled)
	if err := h.funding.Record(h.ctx, "txn_orphan"); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("orphan collection: %v, want ErrUnknownAccount", err)
	}
}

// Money in is spendable by both rails immediately — the whole reason the
// balance was converged.
func TestFunding_CreditedMoneyIsSpendableByCard(t *testing.T) {
	h := newFundingHarness(t, 0)
	acct, err := h.funding.EnsureAccount(h.ctx, h.customerID)
	if err != nil {
		t.Fatal(err)
	}
	h.rail.pay("txn_spend", acct.AccountNumber, 50_000_00, providers.CollectionSettled)
	if err := h.funding.Record(h.ctx, "txn_spend"); err != nil {
		t.Fatal(err)
	}

	// A card can now be issued (its fee comes out of the money that arrived)
	// and used.
	card := h.issueVirtual()
	d, err := h.cards.Authorize(h.ctx, AuthRequest{
		ProviderAuthID: "spend_1", CardID: card.ID,
		AmountMinor: 20_000_00, Currency: "NGN",
		MerchantName: "Shoprite", MerchantMCC: "5411", EntryMode: "chip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Approved {
		t.Fatalf("a card could not spend money that had just arrived: %s", d.DeclineReason)
	}
}

// An account issued by a test issuer is a real-looking number that swallows
// real money. The app has to be able to say so.
func TestFunding_LiveFlagTellsTheTruth(t *testing.T) {
	h := newFundingHarness(t, 0)
	h.rail.sandbox = true

	acct, err := h.funding.EnsureAccount(h.ctx, h.customerID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Live {
		t.Error("a sandbox account reported itself live")
	}

	// A live key issuing at a bank whose name says test is still not live.
	if isLiveIssuer(false, "Providus Test Bank") {
		t.Error("an account at a test bank reported itself live")
	}
	if !isLiveIssuer(false, "Providus Bank") {
		t.Error("a real account reported itself not live")
	}
}
