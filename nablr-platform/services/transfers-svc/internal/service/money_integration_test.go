package service

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/providers"
)

const ngn = "NGN"

// generousLimits is a per-transaction / daily / balance-cap triple large enough
// that no test amount is refused for being too big.
func (h *harness) generousLimits(user uuid.UUID) {
	h.setLimits(user, ngn, 1_000_000_00, 10_000_000_00, 100_000_000_00)
}

// bankTransfer sets up a funded sender with an aged bank payee and initiates a
// payout of amountMinor. It returns the pending transfer and the sender's wallet
// id. The payout is reserved but NOT yet dispatched — the worker does that.
func (h *harness) bankTransfer(amountMinor int64, account, idemKey string) (db.Transfer, uuid.UUID) {
	h.t.Helper()
	sender := uuid.New()
	wallet := h.fundWallet(sender, ngn, 1_000_000_00)
	h.generousLimits(sender)
	payee := h.bankPayee(sender, account, "058")
	tr, err := h.svc.Transfer(h.ctx, sender, TransferInput{
		BeneficiaryID: payee, AmountMinor: amountMinor, Currency: ngn, IdempotencyKey: idemKey, PIN: testPIN,
	})
	if err != nil {
		h.t.Fatalf("initiate bank transfer: %v", err)
	}
	return tr, wallet
}

// --- The double-pay fix, both halves -------------------------------------

// No intent row means the rail was provably never called, so the sweep may hand
// the money back. This is the case the sweep exists for, and it must still work.
func TestIntegration_ReleaseUndispatched_NoIntentRow_Refunds(t *testing.T) {
	h := newHarness(t, time.Hour)
	tr, wallet := h.bankTransfer(10000, "0123456781", "no-intent-key")

	// Money is reserved: available fell, reserved rose.
	avail, reserved := h.balance(wallet)
	if reserved != 10000 {
		t.Fatalf("after initiate: reserved=%d, want 10000", reserved)
	}
	if avail != 1_000_000_00-10000 {
		t.Fatalf("after initiate: available=%d, want %d", avail, 1_000_000_00-10000)
	}

	// It is old enough to sweep, and it has no dispatch-intent row.
	h.backdate(tr.ID, time.Hour)
	released, err := h.svc.ReleaseUndispatched(h.ctx, time.Minute, 10)
	if err != nil {
		t.Fatalf("ReleaseUndispatched: %v", err)
	}
	if released != 1 {
		t.Fatalf("released=%d, want 1: a transfer never sent to the rail must be refunded", released)
	}

	got := h.transfer(tr.ID)
	if got.Status != "failed" {
		t.Errorf("status=%q, want failed", got.Status)
	}
	avail, reserved = h.balance(wallet)
	if reserved != 0 {
		t.Errorf("reserved=%d, want 0: the hold must be released", reserved)
	}
	if avail != 1_000_000_00 {
		t.Errorf("available=%d, want %d: the money must be back", avail, 1_000_000_00)
	}
}

// An intent row with the rail reporting the payout as live means the money may
// be gone. The sweep must NOT release it — it must adopt the transfer into
// processing and leave the funds reserved. This is the exact regression the old
// "refund on timeout alone" code caused, and the assertion that guards it.
func TestIntegration_ReleaseUndispatched_IntentRow_NeverRefunds(t *testing.T) {
	h := newHarness(t, time.Hour)
	tr, wallet := h.bankTransfer(10000, "0123456782", "intent-key")

	// Record that a dispatch was attempted (no reference yet — outcome unknown).
	key := tr.ID.String()
	if _, err := h.svc.q.RecordDispatchIntent(h.ctx, db.RecordDispatchIntentParams{
		TransferID: tr.ID, ProviderName: h.svc.PayoutRailName(), IdempotencyKey: key,
	}); err != nil {
		t.Fatalf("record dispatch intent: %v", err)
	}
	// Make the rail able to confirm it holds this instruction, keyed by the same
	// idempotency key the sweep will query with. A long settle delay keeps it
	// "processing" for the duration of the test.
	if _, err := h.mock.SendPayout(h.ctx, providers.PayoutRequest{
		IdempotencyKey: key, AmountMinor: 10000, Currency: ngn,
		CountryCode: "NG", AccountNumber: "0123456782", BankCode: "058", AccountName: "Test Payee",
	}); err != nil {
		t.Fatalf("prime rail: %v", err)
	}

	h.backdate(tr.ID, time.Hour)
	released, err := h.svc.ReleaseUndispatched(h.ctx, time.Minute, 10)
	if err != nil {
		t.Fatalf("ReleaseUndispatched: %v", err)
	}
	if released != 0 {
		t.Fatalf("released=%d, want 0: a transfer the rail is holding must NEVER be auto-refunded", released)
	}

	got := h.transfer(tr.ID)
	if got.Status == "failed" || got.Status == "refunded" {
		t.Errorf("status=%q: a possibly-dispatched payout must not be failed or refunded by the sweep", got.Status)
	}
	if got.Status != "processing" {
		t.Errorf("status=%q, want processing: the sweep should adopt the rail's reference", got.Status)
	}
	if !got.ProviderReference.Valid || got.ProviderReference.String == "" {
		t.Error("provider_reference must be recorded from the rail")
	}
	_, reserved := h.balance(wallet)
	if reserved != 10000 {
		t.Errorf("reserved=%d, want 10000: the money stays reserved, neither spent nor returned", reserved)
	}
}

// --- Dispatch-time classification ----------------------------------------

// A timeout is retryable and says nothing about what happened. Dispatch must
// leave the payment pending with funds reserved, never refund it.
func TestIntegration_Dispatch_TimeoutLeavesFundsReserved(t *testing.T) {
	h := newHarness(t, time.Hour)
	// suffix 99: the mock times out on the first attempt.
	tr, wallet := h.bankTransfer(10099, "0123456783", "timeout-key")

	if err := h.svc.DispatchOutbox(h.ctx, 10); err != nil {
		t.Fatalf("DispatchOutbox: %v", err)
	}

	got := h.transfer(tr.ID)
	if got.Status != "pending" {
		t.Errorf("status=%q, want pending: a timeout must not resolve the transfer", got.Status)
	}
	_, reserved := h.balance(wallet)
	if reserved != 10099 {
		t.Errorf("reserved=%d, want 10099: a timeout must not release funds", reserved)
	}
}

// ErrDuplicateRequest is the sharpest case: the rail already holds the
// instruction, so refunding would be a *guaranteed* double payment. Dispatch
// must treat it as unknown and keep the money reserved.
func TestIntegration_Dispatch_DuplicateRequestNeverRefunds(t *testing.T) {
	h := newHarness(t, time.Hour)
	account := "0123456784"
	tr, wallet := h.bankTransfer(10000, account, "dup-key")
	h.mock.InjectError(account, providers.ErrDuplicateRequest, "rail already has this instruction")

	if err := h.svc.DispatchOutbox(h.ctx, 10); err != nil {
		t.Fatalf("DispatchOutbox: %v", err)
	}

	got := h.transfer(tr.ID)
	if got.Status == "failed" || got.Status == "refunded" {
		t.Fatalf("status=%q: ErrDuplicateRequest must never trigger a refund", got.Status)
	}
	if got.Status != "pending" {
		t.Errorf("status=%q, want pending", got.Status)
	}
	_, reserved := h.balance(wallet)
	if reserved != 10000 {
		t.Errorf("reserved=%d, want 10000: money must remain reserved on a duplicate-request outcome", reserved)
	}
}

// A clean, non-retryable rejection proves the instruction never executed, so the
// hold IS released — the positive case of the allowlist, end to end.
func TestIntegration_Dispatch_RejectionReleasesHold(t *testing.T) {
	h := newHarness(t, time.Hour)
	// suffix 13: the mock rejects synchronously with ErrRejected.
	tr, wallet := h.bankTransfer(10013, "0123456785", "reject-key")

	if err := h.svc.DispatchOutbox(h.ctx, 10); err != nil {
		t.Fatalf("DispatchOutbox: %v", err)
	}

	got := h.transfer(tr.ID)
	if got.Status != "failed" {
		t.Errorf("status=%q, want failed: a clean rejection releases the payment", got.Status)
	}
	avail, reserved := h.balance(wallet)
	if reserved != 0 {
		t.Errorf("reserved=%d, want 0: a rejected payout returns the hold", reserved)
	}
	if avail != 1_000_000_00 {
		t.Errorf("available=%d, want %d: the money must be back after a rejection", avail, 1_000_000_00)
	}
}

// --- Internal transfer: the ledger must balance, and money moves once --------

func TestIntegration_InternalTransfer_LedgerBalancesAndMovesOnce(t *testing.T) {
	h := newHarness(t, time.Hour)
	sender, recipient := uuid.New(), uuid.New()
	srcWallet := h.fundWallet(sender, ngn, 1_000_000_00)
	dstWallet := h.fundWallet(recipient, ngn, 0)
	h.generousLimits(sender)
	h.generousLimits(recipient)
	payee := h.internalPayee(sender, recipient)

	const amount = 25000
	const key = "internal-key"
	in := TransferInput{BeneficiaryID: payee, AmountMinor: amount, Currency: ngn, IdempotencyKey: key, PIN: testPIN}

	tr, err := h.svc.Transfer(h.ctx, sender, in)
	if err != nil {
		t.Fatalf("internal transfer: %v", err)
	}
	if tr.Status != "completed" {
		t.Fatalf("status=%q, want completed: an internal transfer settles synchronously", tr.Status)
	}

	// The money moved exactly once, in both wallets.
	srcAvail, srcReserved := h.balance(srcWallet)
	if srcAvail != 1_000_000_00-amount || srcReserved != 0 {
		t.Errorf("sender wallet available=%d reserved=%d, want %d/0", srcAvail, srcReserved, 1_000_000_00-amount)
	}
	dstAvail, _ := h.balance(dstWallet)
	if dstAvail != amount {
		t.Errorf("recipient available=%d, want %d", dstAvail, amount)
	}

	// The ledger balances: total debits == total credits.
	debits, credits := h.ledgerSums()
	if debits != credits {
		t.Errorf("ledger debits=%d credits=%d: a double-entry ledger MUST balance to zero", debits, credits)
	}
	if debits != amount {
		t.Errorf("ledger debits=%d, want %d", debits, amount)
	}
	if n := h.countRows("ledger_transactions"); n != 1 {
		t.Errorf("ledger_transactions=%d, want 1", n)
	}

	// Idempotent replay: the same key returns the same transfer and moves no
	// further money.
	replay, err := h.svc.Transfer(h.ctx, sender, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.ID != tr.ID {
		t.Errorf("replay returned a different transfer %s, want %s", replay.ID, tr.ID)
	}
	srcAvail2, _ := h.balance(srcWallet)
	if srcAvail2 != srcAvail {
		t.Errorf("replay moved money: sender available %d -> %d", srcAvail, srcAvail2)
	}
	if n := h.countRows("ledger_transactions"); n != 1 {
		t.Errorf("after replay ledger_transactions=%d, want 1: a replay must not post a second time", n)
	}
}

// Insufficient funds is caught by ReserveWallet updating zero rows, not by a
// read-then-write that could race. No transfer, no hold, no balance change.
func TestIntegration_InsufficientFunds(t *testing.T) {
	h := newHarness(t, time.Hour)
	sender := uuid.New()
	wallet := h.fundWallet(sender, ngn, 5000)
	h.generousLimits(sender)
	payee := h.bankPayee(sender, "0123456786", "058")

	_, err := h.svc.Transfer(h.ctx, sender, TransferInput{
		BeneficiaryID: payee, AmountMinor: 10000, Currency: ngn, IdempotencyKey: "nsf-key", PIN: testPIN,
	})
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("err=%v, want ErrInsufficientFunds", err)
	}
	avail, reserved := h.balance(wallet)
	if avail != 5000 || reserved != 0 {
		t.Errorf("wallet available=%d reserved=%d, want 5000/0: a refused transfer touches nothing", avail, reserved)
	}
	if n := h.countRows("transfers"); n != 0 {
		t.Errorf("transfers=%d, want 0: an underfunded transfer must not be created", n)
	}
	if n := h.countRows("holds"); n != 0 {
		t.Errorf("holds=%d, want 0", n)
	}
}

// A webhook is acted on once. The second delivery of the same (rail, event_id)
// is reported as a duplicate so the caller does not settle twice.
func TestIntegration_WebhookDedupe(t *testing.T) {
	h := newHarness(t, time.Hour)
	first, err := h.svc.AcceptProviderWebhook(h.ctx, "evt-1", "payout.settled", "sha", []byte(`{}`), true)
	if err != nil {
		t.Fatalf("first webhook: %v", err)
	}
	if !first {
		t.Fatal("first delivery must be reported as new")
	}
	again, err := h.svc.AcceptProviderWebhook(h.ctx, "evt-1", "payout.settled", "sha", []byte(`{}`), true)
	if err != nil {
		t.Fatalf("second webhook: %v", err)
	}
	if again {
		t.Fatal("a repeated event_id must be reported as a duplicate, not acted on twice")
	}
}
