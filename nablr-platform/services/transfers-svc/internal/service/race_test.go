package service

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	db "nabla/transfers-svc/db/sqlc"
)

// A customer who taps Send twice — or whose client retries mid-flight — sends
// one payment. N concurrent calls with the same idempotency key must all return
// the same transfer, and the wallet must be debited exactly once.
func TestRace_DoubleTapSendMovesMoneyOnce(t *testing.T) {
	h := newHarness(t, time.Hour)
	sender, recipient := uuid.New(), uuid.New()
	srcWallet := h.fundWallet(sender, ngn, 1_000_000_00)
	h.fundWallet(recipient, ngn, 0)
	h.generousLimits(sender)
	h.generousLimits(recipient)
	payee := h.internalPayee(sender, recipient)

	const amount = 25000
	in := TransferInput{BeneficiaryID: payee, AmountMinor: amount, Currency: ngn, IdempotencyKey: "double-tap", PIN: testPIN}

	const n = 8
	var wg sync.WaitGroup
	ids := make([]uuid.UUID, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			tr, err := h.svc.Transfer(h.ctx, sender, in)
			ids[i], errs[i] = tr.ID, err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d errored: %v (a double-tap must resolve, not fail)", i, err)
		}
		if ids[i] != ids[0] {
			t.Fatalf("goroutine %d got transfer %s, goroutine 0 got %s: all taps must return one payment", i, ids[i], ids[0])
		}
	}

	avail, reserved := h.balance(srcWallet)
	if avail != 1_000_000_00-amount {
		t.Errorf("sender available=%d, want %d: the money must move exactly once", avail, 1_000_000_00-amount)
	}
	if reserved != 0 {
		t.Errorf("sender reserved=%d, want 0", reserved)
	}
	if c := h.countRows("transfers"); c != 1 {
		t.Errorf("transfers=%d, want 1: the unique index must collapse the race to one row", c)
	}
	if c := h.countRows("ledger_transactions"); c != 1 {
		t.Errorf("ledger_transactions=%d, want 1: money posted once", c)
	}
}

// Two sweeps can find the same expired hold at the same time. Only one may
// return the money; the loser must be turned away by the RowsAffected guard, or
// the customer is credited twice for one release.
func TestRace_ConcurrentHoldReleaseCreditsOnce(t *testing.T) {
	h := newHarness(t, time.Hour)
	user := uuid.New()
	wallet := h.fundWallet(user, ngn, 100000)

	// Reserve 40000 and open a hold against it, the state a pending payout leaves.
	const amount = 40000
	if n, err := h.svc.q.ReserveWallet(h.ctx, db.ReserveWalletParams{WalletID: wallet, AmountMinor: amount}); err != nil || n != 1 {
		t.Fatalf("reserve: n=%d err=%v", n, err)
	}
	hold, err := h.svc.q.CreateHold(h.ctx, db.CreateHoldParams{WalletID: wallet, AmountMinor: amount, ExpiresAt: time.Now()})
	if err != nil {
		t.Fatalf("create hold: %v", err)
	}

	// Two releasers race on the one hold.
	const n = 2
	var wg sync.WaitGroup
	results := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = releaseReservation(h.ctx, h.svc.q, hold.ID, wallet, amount)
		}(i)
	}
	wg.Wait()

	winners, losers := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case err == ErrHoldAlreadyResolved:
			losers++
		default:
			t.Fatalf("unexpected release error: %v", err)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("winners=%d losers=%d, want exactly 1 each: only one release may credit the wallet", winners, losers)
	}

	// Credited exactly once: available is back to the full 100000, reserved 0.
	avail, reserved := h.balance(wallet)
	if avail != 100000 {
		t.Errorf("available=%d, want 100000: the hold must be returned exactly once, not twice", avail)
	}
	if reserved != 0 {
		t.Errorf("reserved=%d, want 0", reserved)
	}
}
