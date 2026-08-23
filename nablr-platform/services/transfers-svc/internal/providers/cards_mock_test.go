package providers

import (
	"context"
	"testing"
)

func TestMockIssuerAsksForNoKYC(t *testing.T) {
	m := NewMockCardIssuer()
	if IssuerNeedsCardholderKYC(m) {
		t.Fatal("the mock issuer declared cardholder_kyc")
	}
	if IssuerNeedsCardholderKYC(nil) {
		t.Fatal("a nil issuer was said to need cardholder KYC")
	}
}

func TestMockIssueIsIdempotent(t *testing.T) {
	m := NewMockCardIssuer()
	ctx := context.Background()

	first, err := m.IssueCard(ctx, IssueCardRequest{IdempotencyKey: "k1", CardType: "virtual"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.IssueCard(ctx, IssueCardRequest{IdempotencyKey: "k1", CardType: "virtual"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderCardID != second.ProviderCardID {
		t.Fatalf("the same idempotency key made two cards: %s and %s",
			first.ProviderCardID, second.ProviderCardID)
	}

	other, err := m.IssueCard(ctx, IssueCardRequest{IdempotencyKey: "k2", CardType: "virtual"})
	if err != nil {
		t.Fatal(err)
	}
	if other.ProviderCardID == first.ProviderCardID {
		t.Fatal("two different keys returned the same card")
	}
}

func TestMockRevealMatchesStoredLast4AndPassesLuhn(t *testing.T) {
	m := NewMockCardIssuer()
	ctx := context.Background()

	issued, err := m.IssueCard(ctx, IssueCardRequest{IdempotencyKey: "k", CardType: "virtual"})
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := m.RevealCard(ctx, issued.ProviderCardID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets.PAN) != 16 {
		t.Fatalf("PAN is %d digits, want 16: %q", len(secrets.PAN), secrets.PAN)
	}
	if got := secrets.PAN[12:]; got != issued.Last4 {
		t.Errorf("revealed PAN ends %q but the stored last4 is %q", got, issued.Last4)
	}
	if !luhnValid(secrets.PAN) {
		t.Errorf("PAN %q does not satisfy Luhn", secrets.PAN)
	}
	if secrets.ExpiryMonth != issued.ExpiryMonth || secrets.ExpiryYear != issued.ExpiryYear {
		t.Errorf("revealed expiry %d/%d differs from the issued %d/%d",
			secrets.ExpiryMonth, secrets.ExpiryYear, issued.ExpiryMonth, issued.ExpiryYear)
	}

	fresh := NewMockCardIssuer()
	again, err := fresh.RevealCard(ctx, issued.ProviderCardID)
	if err != nil {
		t.Fatal(err)
	}
	if again.PAN != secrets.PAN {
		t.Errorf("a restarted mock revealed a different PAN: %q then %q", secrets.PAN, again.PAN)
	}
}

func TestMockSetCardStateSurvivesRestart(t *testing.T) {
	m := NewMockCardIssuer()
	ctx := context.Background()

	issued, err := m.IssueCard(ctx, IssueCardRequest{IdempotencyKey: "k", CardType: "virtual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetCardState(ctx, issued.ProviderCardID, "frozen"); err != nil {
		t.Fatal(err)
	}
	if got := m.StateOf(issued.ProviderCardID); got != "frozen" {
		t.Errorf("state is %q, want frozen", got)
	}

	fresh := NewMockCardIssuer()
	if err := fresh.SetCardState(ctx, issued.ProviderCardID, "terminated"); err != nil {
		t.Errorf("a restarted mock refused a card it had forgotten: %v", err)
	}
	if err := fresh.SetCardState(ctx, "not-a-mock-card", "frozen"); err == nil {
		t.Error("a card id from no known issuer was accepted")
	}
}

func luhnValid(pan string) bool {
	sum, double := 0, false
	for i := len(pan) - 1; i >= 0; i-- {
		d := int(pan[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if double {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}
