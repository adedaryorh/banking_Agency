package service

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"nabla/transfers-svc/internal/providers"
)

var testCustomerID = uuid.MustParse("3f2504e0-4f89-11d3-9a0c-0305e82c3301")

func TestCardFeeMinor(t *testing.T) {
	if got := CardFeeMinor("virtual"); got != 100_00 {
		t.Errorf("virtual card fee = %d kobo, want 10000 (₦100)", got)
	}
	if got := CardFeeMinor("physical"); got != 2_000_00 {
		t.Errorf("physical card fee = %d kobo, want 200000 (₦2,000)", got)
	}
	// Anything unrecognised is priced as the cheap card, never as free: a fee
	// of zero would issue a card nobody paid for.
	if got := CardFeeMinor("something-else"); got != 100_00 {
		t.Errorf("unknown card type fee = %d, want the virtual fee", got)
	}
	if CardCurrency != "NGN" {
		t.Errorf("CardCurrency = %q, want NGN", CardCurrency)
	}
}

// The identity number is the issuer's KYC and travels straight through. It has
// to be the right shape before it goes anywhere.
func TestIdentityNumberShape(t *testing.T) {
	for _, ok := range []string{"12345678901", "00000000000"} {
		if !idNumberPattern.MatchString(ok) {
			t.Errorf("%q should be a valid NIN/BVN", ok)
		}
	}
	for _, bad := range []string{"", "1234567890", "123456789012", "1234567890a", " 12345678901"} {
		if idNumberPattern.MatchString(bad) {
			t.Errorf("%q should not be a valid NIN/BVN", bad)
		}
	}
}

func TestDateOfBirthShape(t *testing.T) {
	if !dobPattern.MatchString("1990-04-02") {
		t.Error("yyyy-mm-dd should be accepted")
	}
	for _, bad := range []string{"", "02/04/1990", "1990-4-2", "1990-04-02T00:00:00Z"} {
		if dobPattern.MatchString(bad) {
			t.Errorf("%q should not be accepted as a date of birth", bad)
		}
	}
}

// billingAddressFor prefers the address given for billing and falls back to
// where the plastic is being couriered — a physical card already carries an
// address, and that is the one a cardholder expects on the account.
func TestBillingAddressPrefersBillingThenDelivery(t *testing.T) {
	billing := &BillingAddress{Line1: "12 Bank St", City: "Lagos", State: "Lagos"}
	delivery := &Delivery{Line1: "9 Courier Rd", City: "Abuja", State: "FCT", Phone: "+234..."}

	got := billingAddressFor(billing, delivery)
	if got.Line1 != "12 Bank St" || got.City != "Lagos" {
		t.Errorf("billing address should win: %+v", got)
	}
	// A country nobody supplied defaults to Nigeria rather than to empty; an
	// issuer given a blank country rejects the cardholder.
	if got.Country != "Nigeria" {
		t.Errorf("country = %q, want Nigeria", got.Country)
	}

	got = billingAddressFor(nil, delivery)
	if got.Line1 != "9 Courier Rd" || got.City != "Abuja" {
		t.Errorf("delivery address should be the fallback: %+v", got)
	}

	// A billing address with no line1 is not an address; fall through to the
	// delivery one rather than sending half of it.
	got = billingAddressFor(&BillingAddress{City: "Lagos"}, delivery)
	if got.Line1 != "9 Courier Rd" {
		t.Errorf("an empty billing line1 should fall through: %+v", got)
	}

	if got = billingAddressFor(nil, nil); got.Line1 != "" {
		t.Errorf("with nothing given the address must be empty, got %+v", got)
	}
}

// An explicitly supplied country is kept: a cardholder who is not in Nigeria
// must not be recorded as though they were.
func TestBillingAddressKeepsSuppliedCountry(t *testing.T) {
	got := billingAddressFor(&BillingAddress{Line1: "1 High St", City: "London", Country: "United Kingdom"}, nil)
	if got.Country != "United Kingdom" {
		t.Errorf("country = %q, want United Kingdom", got.Country)
	}
}

// nullIfEmpty keeps "" out of columns whose CHECK constraints reject it. brand
// and last4 both accept NULL; neither accepts an empty string, and an issuer
// that returned nothing for one would otherwise fail the whole issue.
func TestNullIfEmpty(t *testing.T) {
	if nullIfEmpty("") != nil {
		t.Error("an empty string should become NULL")
	}
	v := nullIfEmpty("visa")
	if v == nil || *v != "visa" {
		t.Errorf("a value should be passed through, got %v", v)
	}
}

// The default issuer name is what a card is stamped with when the customer is
// not on the real-issuer allow list.
func TestDefaultIssuerName(t *testing.T) {
	s := &CardsService{}
	if got := s.DefaultIssuerName(); got != "" {
		t.Errorf("with no issuer the name should be empty, got %q", got)
	}
	s.issuer = providers.NewMockCardIssuer()
	if got := s.DefaultIssuerName(); got != "mock" {
		t.Errorf("DefaultIssuerName() = %q, want mock", got)
	}
}

// issuerFor hands a customer the real issuer only when the predicate says so.
// Every other customer keeps the one they already had.
func TestIssuerForIsGated(t *testing.T) {
	mock := providers.NewMockCardIssuer()
	s := &CardsService{issuer: mock}

	if s.issuerFor(context.Background(), testCustomerID) != mock {
		t.Fatal("with no preferred issuer set, the default must be used")
	}

	// A preferred issuer with no predicate must NOT take effect: a rollout
	// with nobody named is a rollout to nobody, not to everybody.
	other := providers.NewMockCardIssuer()
	s.SetPreferredIssuer(other, nil)
	if s.issuerFor(context.Background(), testCustomerID) != mock {
		t.Fatal("a preferred issuer with a nil predicate was used for everyone")
	}

	s.SetPreferredIssuer(other, func(context.Context, uuid.UUID) bool { return false })
	if s.issuerFor(context.Background(), testCustomerID) != mock {
		t.Fatal("a customer the predicate refused got the real issuer")
	}

	s.SetPreferredIssuer(other, func(context.Context, uuid.UUID) bool { return true })
	if s.issuerFor(context.Background(), testCustomerID) != other {
		t.Fatal("a customer the predicate allowed did not get the real issuer")
	}
}
