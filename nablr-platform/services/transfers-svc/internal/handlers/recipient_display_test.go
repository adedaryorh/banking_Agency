package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "nabla/transfers-svc/db/sqlc"
)

func ptext(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// recipientForBeneficiary is what the confirm/receipt/details screens read, so
// its projection of each payee type is pinned here.
func TestRecipientForBeneficiary(t *testing.T) {
	t.Run("internal shows Nablr, number and @handle", func(t *testing.T) {
		r := recipientForBeneficiary(db.Beneficiary{
			Type: "internal", AccountName: "Ada Lovelace",
			RecipientAccountNumber: ptext("8012345678"), RecipientUsername: ptext("ada"),
		})
		if r.Type != "internal" || r.Name != "Ada Lovelace" || r.Institution != "Nablr" {
			t.Fatalf("internal head wrong: %+v", r)
		}
		if r.AccountNumber != "8012345678" {
			t.Errorf("account number = %q, want raw Nablr number", r.AccountNumber)
		}
		if r.Username != "@ada" {
			t.Errorf("username = %q, want @ada", r.Username)
		}
	})

	t.Run("bank shows bank name and masked last4", func(t *testing.T) {
		r := recipientForBeneficiary(db.Beneficiary{
			Type: "bank", AccountName: "typed name", VerifiedName: ptext("ADA LOVELACE"),
			BankName: ptext("Zenith Bank"), AccountNumberLast4: ptext("6789"),
		})
		if r.Institution != "Zenith Bank" {
			t.Errorf("institution = %q, want Zenith Bank", r.Institution)
		}
		if r.AccountNumber != "••••6789" {
			t.Errorf("account number = %q, want masked last4", r.AccountNumber)
		}
		// Verified name outranks the typed account name for a bank payee.
		if r.Name != "ADA LOVELACE" {
			t.Errorf("name = %q, want verified name", r.Name)
		}
		if r.Username != "" {
			t.Errorf("bank payee should have no @handle, got %q", r.Username)
		}
	})

	t.Run("nickname outranks everything", func(t *testing.T) {
		r := recipientForBeneficiary(db.Beneficiary{
			Type: "internal", Nickname: ptext("Mum"), VerifiedName: ptext("Grace Hopper"),
			AccountName: "Grace Hopper", RecipientAccountNumber: ptext("8000000000"),
		})
		if r.Name != "Mum" {
			t.Errorf("name = %q, want nickname Mum", r.Name)
		}
	})

	t.Run("legacy internal without captured identifiers still renders name", func(t *testing.T) {
		r := recipientForBeneficiary(db.Beneficiary{Type: "internal", AccountName: "Old Payee"})
		if r.Name != "Old Payee" || r.Institution != "Nablr" {
			t.Fatalf("legacy internal wrong: %+v", r)
		}
		if r.AccountNumber != "" || r.Username != "" {
			t.Errorf("legacy internal should omit number/handle, got %+v", r)
		}
	})
}

func TestStatusLabel(t *testing.T) {
	cases := map[string]string{
		"created":         "Payment Initiated",
		"pending":         "Payment Initiated",
		"processing":      "Processing",
		"under_review":    "Under Review",
		"completed":       "Completed",
		"failed":          "Failed",
		"cancelled":       "Cancelled",
		"reversed":        "Reversed",
		"refunded":        "Refunded",
		"some_new_status": "some_new_status", // unmapped falls back to raw
	}
	for in, want := range cases {
		if got := statusLabel(in); got != want {
			t.Errorf("statusLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInitialsOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Ada Lovelace", "AL"},
		{"madonna", "M"},
		{"  Grace   Hopper  ", "GH"},    // extra whitespace collapsed
		{"Chinua Achebe Okonkwo", "CA"}, // capped at two
		{"@ada", "A"},                   // leading non-letter skipped
		{"", ""},
	}
	for _, c := range cases {
		if got := initialsOf(c.in); got != c.want {
			t.Errorf("initialsOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToRecentRecipientDTO(t *testing.T) {
	ts := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	t.Run("internal", func(t *testing.T) {
		id := uuid.New()
		dto := toRecentRecipientDTO(db.RecentRecipientsRow{
			ID: id, Type: "internal", AccountName: "Ada Lovelace",
			RecipientAccountNumber: ptext("8012345678"), RecipientUsername: ptext("ada"),
			LastSentAt: ts, TransferCount: 3,
		})
		if dto.BeneficiaryID != id.String() {
			t.Errorf("beneficiary id = %q, want %q", dto.BeneficiaryID, id.String())
		}
		if dto.Name != "Ada Lovelace" || dto.Initials != "AL" || dto.Institution != "Nablr" {
			t.Fatalf("internal head wrong: %+v", dto)
		}
		if dto.AccountNumber != "8012345678" || dto.Username != "@ada" {
			t.Errorf("internal identifiers wrong: %+v", dto)
		}
		if dto.LastSentAt != "2026-08-20T10:00:00Z" {
			t.Errorf("last_sent_at = %q, want RFC3339 UTC", dto.LastSentAt)
		}
		if dto.TransferCount != 3 {
			t.Errorf("transfer_count = %d, want 3", dto.TransferCount)
		}
	})

	t.Run("bank uses verified name and masks account", func(t *testing.T) {
		dto := toRecentRecipientDTO(db.RecentRecipientsRow{
			ID: uuid.New(), Type: "bank", AccountName: "typed",
			VerifiedName: ptext("ADA L"), BankName: ptext("Zenith Bank"),
			AccountNumberLast4: ptext("6789"), LastSentAt: ts, TransferCount: 1,
		})
		if dto.Name != "ADA L" || dto.Institution != "Zenith Bank" || dto.AccountNumber != "••••6789" {
			t.Fatalf("bank projection wrong: %+v", dto)
		}
	})

	t.Run("nickname outranks verified and account", func(t *testing.T) {
		dto := toRecentRecipientDTO(db.RecentRecipientsRow{
			ID: uuid.New(), Type: "internal", Nickname: ptext("Mum"),
			VerifiedName: ptext("Grace Hopper"), AccountName: "Grace Hopper", LastSentAt: ts,
		})
		if dto.Name != "Mum" {
			t.Errorf("name = %q, want nickname", dto.Name)
		}
	})
}
