package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "nabla/transfers-svc/db/sqlc"
)

// ptext is defined in recipient_display_test.go (same package) and reused here.

func TestEffectivePaymentRequestStatus(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	past := pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}
	future := pgtype.Timestamptz{Time: now.Add(time.Hour), Valid: true}

	cases := []struct {
		name   string
		status string
		exp    pgtype.Timestamptz
		want   string
	}{
		{"pending not yet expired stays pending", "pending", future, "pending"},
		{"pending past expiry renders expired", "pending", past, "expired"},
		{"pending with no expiry stays pending", "pending", pgtype.Timestamptz{}, "pending"},
		{"paid is never overridden by expiry", "paid", past, "paid"},
		{"declined is never overridden by expiry", "declined", past, "declined"},
		{"cancelled is never overridden by expiry", "cancelled", past, "cancelled"},
	}
	for _, c := range cases {
		got := effectivePaymentRequestStatus(db.PaymentRequest{Status: c.status, ExpiresAt: c.exp}, now)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPartyDTO(t *testing.T) {
	t.Run("name and handle produce initials from the name", func(t *testing.T) {
		p := partyDTO("Ada Lovelace", "ada")
		if p.Name != "Ada Lovelace" || p.Username != "@ada" || p.Initials != "AL" {
			t.Fatalf("got %+v", p)
		}
	})
	t.Run("handle only falls back to handle initials", func(t *testing.T) {
		p := partyDTO("", "ada")
		if p.Name != "" || p.Username != "@ada" || p.Initials != "A" {
			t.Fatalf("got %+v", p)
		}
	})
	t.Run("empty stays empty", func(t *testing.T) {
		p := partyDTO("  ", "")
		if p.Name != "" || p.Username != "" || p.Initials != "" {
			t.Fatalf("got %+v", p)
		}
	})
}

// toPaymentRequestDTO carries the direction, action flags and effective status
// the client renders buttons from, so those are pinned here per party and state.
func TestToPaymentRequestDTO(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	requester := uuid.New()
	payer := uuid.New()
	created := now.Add(-2 * time.Hour)

	base := db.PaymentRequest{
		ID:                uuid.New(),
		RequesterUserID:   requester,
		PayerUserID:       payer,
		AmountMinor:       500000, // ₦5,000.00
		Currency:          "NGN",
		Note:              ptext("lunch"),
		Status:            "pending",
		RequesterName:     ptext("Ada Lovelace"),
		RequesterUsername: ptext("ada"),
		PayerName:         ptext("Grace Hopper"),
		PayerUsername:     ptext("grace"),
		ExpiresAt:         pgtype.Timestamptz{Time: now.Add(24 * time.Hour), Valid: true},
		CreatedAt:         created,
		UpdatedAt:         created,
	}

	t.Run("incoming pending: the payer may pay or decline, not cancel", func(t *testing.T) {
		dto := toPaymentRequestDTO(base, payer, now)
		if dto.Direction != "incoming" {
			t.Errorf("direction = %q, want incoming", dto.Direction)
		}
		if !dto.CanPay || !dto.CanDecline || dto.CanCancel {
			t.Errorf("payer actions wrong: pay=%v decline=%v cancel=%v", dto.CanPay, dto.CanDecline, dto.CanCancel)
		}
		if dto.Status != "pending" {
			t.Errorf("status = %q, want pending", dto.Status)
		}
		if dto.Amount.Formatted != "NGN 5000.00" {
			t.Errorf("amount = %q, want NGN 5000.00", dto.Amount.Formatted)
		}
		if dto.Note != "lunch" {
			t.Errorf("note = %q", dto.Note)
		}
		if dto.Requester.Username != "@ada" || dto.Payer.Username != "@grace" {
			t.Errorf("party handles wrong: %+v / %+v", dto.Requester, dto.Payer)
		}
	})

	t.Run("outgoing pending: the requester may only cancel", func(t *testing.T) {
		dto := toPaymentRequestDTO(base, requester, now)
		if dto.Direction != "outgoing" {
			t.Errorf("direction = %q, want outgoing", dto.Direction)
		}
		if dto.CanPay || dto.CanDecline || !dto.CanCancel {
			t.Errorf("requester actions wrong: pay=%v decline=%v cancel=%v", dto.CanPay, dto.CanDecline, dto.CanCancel)
		}
	})

	t.Run("a pending request past expiry offers no actions", func(t *testing.T) {
		r := base
		r.ExpiresAt = pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true}
		dto := toPaymentRequestDTO(r, payer, now)
		if dto.Status != "expired" {
			t.Errorf("status = %q, want expired", dto.Status)
		}
		if dto.CanPay || dto.CanDecline || dto.CanCancel {
			t.Errorf("expired request must offer no actions, got pay=%v decline=%v cancel=%v", dto.CanPay, dto.CanDecline, dto.CanCancel)
		}
	})

	t.Run("paid links the transfer and offers no actions", func(t *testing.T) {
		tid := uuid.New()
		r := base
		r.Status = "paid"
		r.TransferID = pgtype.UUID{Bytes: tid, Valid: true}
		dto := toPaymentRequestDTO(r, payer, now)
		if dto.Status != "paid" {
			t.Errorf("status = %q, want paid", dto.Status)
		}
		if dto.TransferID != tid.String() {
			t.Errorf("transfer_id = %q, want %q", dto.TransferID, tid.String())
		}
		if dto.CanPay || dto.CanDecline || dto.CanCancel {
			t.Errorf("paid request must offer no actions")
		}
	})

	t.Run("declined carries the reason and is not cancellable", func(t *testing.T) {
		r := base
		r.Status = "declined"
		r.DeclineReason = ptext("not right now")
		dto := toPaymentRequestDTO(r, requester, now)
		if dto.Status != "declined" || dto.DeclineReason != "not right now" {
			t.Errorf("got status=%q reason=%q", dto.Status, dto.DeclineReason)
		}
		if dto.CanCancel {
			t.Errorf("a declined request must not be cancellable")
		}
	})
}
