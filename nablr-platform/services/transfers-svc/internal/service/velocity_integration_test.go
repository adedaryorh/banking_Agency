package service

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	db "nabla/transfers-svc/db/sqlc"
)

func (h *harness) velocitySender(fund int64) (sender, wallet, payee uuid.UUID) {
	h.t.Helper()
	sender = uuid.New()
	wallet = h.fundWallet(sender, ngn, fund)
	h.generousLimits(sender)
	payee = h.bankPayee(sender, "0123456790", "058")
	return sender, wallet, payee
}

// mustTransfer initiates a bank payout that is expected to succeed.
func (h *harness) mustTransfer(sender, payee uuid.UUID, amount int64, key string) db.Transfer {
	h.t.Helper()
	tr, err := h.svc.Transfer(h.ctx, sender, TransferInput{
		BeneficiaryID: payee, AmountMinor: amount, Currency: ngn, IdempotencyKey: key, PIN: testPIN,
	})
	if err != nil {
		h.t.Fatalf("transfer %q (amount %d) should have succeeded: %v", key, amount, err)
	}
	return tr
}

// transferErr initiates a bank payout and returns only the error, for the cases
// that must be refused.
func (h *harness) transferErr(sender, payee uuid.UUID, amount int64, key string) error {
	h.t.Helper()
	_, err := h.svc.Transfer(h.ctx, sender, TransferInput{
		BeneficiaryID: payee, AmountMinor: amount, Currency: ngn, IdempotencyKey: key, PIN: testPIN,
	})
	return err
}

func TestIntegration_Velocity_WeeklyOutbound(t *testing.T) {
	h := newHarness(t, time.Hour)
	sender, wallet, payee := h.velocitySender(1_000_000_00)
	h.setLimit(sender, ngn, limitWeeklyOutbound, 30000)

	// First payout consumes 20000 of the 30000 weekly allowance.
	h.mustTransfer(sender, payee, 20000, "wk-1")
	if _, reserved := h.balance(wallet); reserved != 20000 {
		t.Fatalf("after first transfer reserved=%d, want 20000", reserved)
	}

	// A second 20000 would make the week 40000 > 30000: refused.
	if err := h.transferErr(sender, payee, 20000, "wk-2"); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("second transfer err=%v, want ErrLimitExceeded", err)
	}
	// The refusal moved nothing: still one transfer, still 20000 reserved.
	if n := h.countRows("transfers"); n != 1 {
		t.Errorf("transfers=%d, want 1: a transfer refused on velocity must not be created", n)
	}
	if _, reserved := h.balance(wallet); reserved != 20000 {
		t.Errorf("reserved=%d, want 20000: a refused transfer reserves nothing", reserved)
	}

	// A 10000 payout lands the week exactly on the 30000 ceiling: allowed, because
	// the ceiling is inclusive (breach is strictly over, not equal).
	h.mustTransfer(sender, payee, 10000, "wk-3")

	// And one more kobo is now over: refused.
	if err := h.transferErr(sender, payee, 1, "wk-4"); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("transfer past the exact ceiling err=%v, want ErrLimitExceeded", err)
	}
}

// A monthly ceiling behaves the same over the calendar-month window, and is
// independent of the weekly overlay (here left unset, i.e. unbounded).
func TestIntegration_Velocity_MonthlyOutbound(t *testing.T) {
	h := newHarness(t, time.Hour)
	sender, _, payee := h.velocitySender(1_000_000_00)
	h.setLimit(sender, ngn, limitMonthlyOutbound, 50000)

	h.mustTransfer(sender, payee, 30000, "mo-1")
	if err := h.transferErr(sender, payee, 30000, "mo-2"); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("second transfer err=%v, want ErrLimitExceeded (60000 > 50000)", err)
	}
	// Exactly filling the remaining 20000 is allowed.
	h.mustTransfer(sender, payee, 20000, "mo-3")
	if n := h.countRows("transfers"); n != 2 {
		t.Errorf("transfers=%d, want 2: only the two within-ceiling payments exist", n)
	}
}

// A daily_count ceiling caps the NUMBER of transfers in a day regardless of
// amount: after the allowance is used, even a one-kobo payment is refused.
func TestIntegration_Velocity_DailyCount(t *testing.T) {
	h := newHarness(t, time.Hour)
	sender, _, payee := h.velocitySender(1_000_000_00)
	h.setCountLimit(sender, ngn, limitDailyCount, 2)

	h.mustTransfer(sender, payee, 10000, "dc-1")
	h.mustTransfer(sender, payee, 10000, "dc-2")

	// The third transfer is over the count even though the amount is tiny and both
	// the per-transaction and daily-amount ceilings have plenty of room.
	if err := h.transferErr(sender, payee, 1, "dc-3"); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("third transfer err=%v, want ErrLimitExceeded (count 3 > 2)", err)
	}
	if n := h.countRows("transfers"); n != 2 {
		t.Errorf("transfers=%d, want 2: the third transfer must not be created", n)
	}
}

// With no velocity overlay on record, the transfer path is unbounded on the
// weekly / monthly / count axes and only the tier amount ceilings apply. This is
// the fail-open half of the invariant: a control nobody set does not refuse.
func TestIntegration_Velocity_Unset_FailsOpen(t *testing.T) {
	h := newHarness(t, time.Hour)
	sender, _, payee := h.velocitySender(1_000_000_00)
	// No setLimit(weekly/monthly) and no setCountLimit: overlays absent.

	// Many small transfers, well past any default velocity a real tier might carry,
	// all succeed because no overlay is configured to bound them.
	for i, key := range []string{"open-1", "open-2", "open-3", "open-4", "open-5"} {
		h.mustTransfer(sender, payee, 5000, key)
		if n := h.countRows("transfers"); n != int64(i+1) {
			t.Fatalf("after %s transfers=%d, want %d", key, n, i+1)
		}
	}
}
