package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"nabla/transfers-svc/internal/platform/clock"
	platform "nabla/transfers-svc/internal/platform/db"
)

func TestToNUBAN(t *testing.T) {

	cases := []struct{ in, want string }{
		{"8031234567", "8031234567"},    // already a NUBAN
		{"08031234567", "8031234567"},   // local phone
		{"2348031234567", "8031234567"}, // international
		{"23408031234567", "8031234567"},
		{"12", "12"},       // not ten digits: passes through unreduced
		{"08123", "08123"}, // fragment stays a fragment
	}
	for _, c := range cases {
		if got := toNUBAN(c.in); got != c.want {
			t.Errorf("toNUBAN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClassifyPay(t *testing.T) {
	cases := []struct {
		q, digits, want string
	}{
		{"@bode", "", "handle"},
		{"Bode@example.com", "", "email"},
		{"Bode", "", "name"},
		{"8031234567", "8031234567", "number"},
		{"081234567", "081234567", "partial_number"},
		{"", "", "empty"},
	}
	for _, c := range cases {
		if got := classifyPay(c.q, c.digits); got != c.want {
			t.Errorf("classifyPay(%q,%q) = %q, want %q", c.q, c.digits, got, c.want)
		}
	}
}

func TestCollectionCallbackOutcome(t *testing.T) {
	cases := []struct {
		name             string
		err              error
		wantStatus       string
		wantNoteNonEmpty bool
	}{
		{"applied", nil, "processed", false},
		{"already recorded", ErrAlreadyRecorded, "processed", false},
		{"not yet settled", ErrNotSettled, "received", true},
		{"unknown account", ErrUnknownAccount, "failed", true},
		{"generic failure", errors.New("boom"), "failed", true},
	}
	for _, c := range cases {
		status, note := CollectionCallbackOutcome(c.err)
		if status != c.wantStatus {
			t.Errorf("%s: status = %q, want %q", c.name, status, c.wantStatus)
		}
		if (note != "") != c.wantNoteNonEmpty {
			t.Errorf("%s: note present = %v, want %v (note=%q)", c.name, note != "", c.wantNoteNonEmpty, note)
		}
	}
}

// resolveFake is the subset of identity-svc a resolve test needs: two lookup
// keys (number and username) plus the KYC call that decorates the name.
type resolveFake struct {
	byNumber map[string]UserProfile
	byUser   map[string]UserProfile
}

func (f *resolveFake) VerifyPIN(context.Context, uuid.UUID, string) (bool, error) { return true, nil }
func (f *resolveFake) GetUser(_ context.Context, u uuid.UUID) (UserProfile, error) {
	if p, ok := f.byUser[u.String()]; ok {
		return p, nil
	}
	return UserProfile{}, ErrIdentityUserNotFound
}
func (f *resolveFake) GetKYCProfile(_ context.Context, u uuid.UUID) (KYCProfile, error) {
	return KYCProfile{UserID: u.String(), FirstName: "Ada", LastName: "Eze"}, nil
}
func (f *resolveFake) GetUserByUsername(_ context.Context, username string) (UserProfile, error) {
	if p, ok := f.byUser[username]; ok {
		return p, nil
	}
	return UserProfile{}, ErrIdentityUserNotFound
}
func (f *resolveFake) GetUserByAccountNumber(_ context.Context, accountNumber string) (UserProfile, error) {
	if p, ok := f.byNumber[accountNumber]; ok {
		return p, nil
	}
	return UserProfile{}, ErrIdentityUserNotFound
}

var (
	adaID = "11111111-1111-1111-1111-111111111111"
	bode  = resolveFake{
		byNumber: map[string]UserProfile{
			"8031234567": {UserID: adaID, NablrUsername: "ada", AccountNumber: "8031234567", Status: "active"},
		},
		byUser: map[string]UserProfile{
			"ada": {UserID: adaID, NablrUsername: "ada", AccountNumber: "8031234567", Status: "active"},
		},
	}
)

func TestFindNablrResolvesViaIdentity(t *testing.T) {
	svc := &Service{identity: &bode}

	t.Run("by number", func(t *testing.T) {
		got := svc.findNablr(context.Background(), "08031234567", "8031234567", "00000000-0000-0000-0000-000000000001")
		if len(got) != 1 {
			t.Fatalf("want 1 match, got %d", len(got))
		}
		m := got[0]
		if m.CustomerID != adaID {
			t.Errorf("customer = %q, want %q", m.CustomerID, adaID)
		}
		if m.MatchedOn != "their Nablr number" {
			t.Errorf("matched_on = %q", m.MatchedOn)
		}
		if m.Name != "Ada Eze" {
			t.Errorf("name = %q, want \"Ada Eze\" (decorated from KYC)", m.Name)
		}
	})

	t.Run("never resolves the payer themselves", func(t *testing.T) {
		if got := svc.findNablr(context.Background(), "08031234567", "8031234567", adaID); len(got) != 0 {
			t.Fatalf("self was not excluded: %+v", got)
		}
	})

	t.Run("by handle", func(t *testing.T) {
		got := svc.findNablr(context.Background(), "@ada", "", "00000000-0000-0000-0000-000000000001")
		if len(got) != 1 {
			t.Fatalf("want 1 match, got %d", len(got))
		}
		if got[0].MatchedOn != "their @handle" {
			t.Errorf("matched_on = %q, want \"their @handle\"", got[0].MatchedOn)
		}
	})

	t.Run("empty when identity has no match", func(t *testing.T) {
		if got := svc.findNablr(context.Background(), "someone-else", "", ""); len(got) != 0 {
			t.Fatalf("want 0 matches, got %d", len(got))
		}
	})

	t.Run("empty when identity is not wired", func(t *testing.T) {
		if got := (&Service{}).findNablr(context.Background(), "@ada", "", ""); len(got) != 0 {
			t.Fatalf("want 0 matches without an identity client, got %d", len(got))
		}
	})
}

func TestCallerWallets(t *testing.T) {
	t.Run("nil pool returns empty, never panics", func(t *testing.T) {
		if got := (&Service{}).callerWallets(context.Background(), adaID); len(got) != 0 {
			t.Fatalf("want empty, got %d wallets", len(got))
		}
	})

	t.Run("unparsable customer returns empty", func(t *testing.T) {
		svc := &Service{}
		svc.pool = nil
		if got := svc.callerWallets(context.Background(), "not-a-uuid"); len(got) != 0 {
			t.Fatalf("want empty, got %d wallets", len(got))
		}
	})

	t.Run("no wallet for the customer returns empty", func(t *testing.T) {
		h := newHarness(t, time.Hour)
		svc := &Service{pool: h.pool}
		if got := svc.callerWallets(h.ctx, uuid.NewString()); len(got) != 0 {
			t.Fatalf("want empty, got %d wallets", len(got))
		}
	})

	t.Run("returns the caller's wallets with live ledger balances", func(t *testing.T) {
		h := newHarness(t, time.Hour)
		svc := &Service{pool: h.pool}

		// OpenWallet is the production wallet-creation path: it creates the
		// backing ledger account and stamps both user_id and customer_id, which
		// is the shape wallets have in the live database.
		user := uuid.New()
		raw := platform.NewAdapter(h.pool)
		opened, err := NewWalletService(raw, clock.RealClock{}).OpenWallet(h.ctx, user, "NGN", "")
		if err != nil {
			t.Fatalf("open wallet: %v", err)
		}
		if _, err := h.pool.Exec(h.ctx,
			`UPDATE ledger_accounts SET balance_minor = 5000000, available_minor = 5000000, reserved_minor = 0 WHERE id = $1`, opened.LedgerAccountID); err != nil {
			t.Fatalf("seed balance: %v", err)
		}

		got := svc.callerWallets(h.ctx, user.String())
		if len(got) != 1 {
			t.Fatalf("want 1 wallet, got %d", len(got))
		}
		if got[0].AvailableMinor != 5_000_000 {
			t.Errorf("available = %d, want 5000000 (read through the ledger)", got[0].AvailableMinor)
		}
		if got[0].Currency != "NGN" {
			t.Errorf("currency = %q, want NGN", got[0].Currency)
		}
	})
}
