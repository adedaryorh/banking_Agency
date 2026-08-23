package providers

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"nabla/transfers-svc/internal/platform/id"
)

type MockCardIssuer struct {
	mu     sync.Mutex
	byKey  map[string]*IssuedCard
	states map[string]string
	seq    int
}

func NewMockCardIssuer() *MockCardIssuer {
	return &MockCardIssuer{byKey: map[string]*IssuedCard{}, states: map[string]string{}}
}

func (m *MockCardIssuer) Info() CardIssuerInfo {
	return CardIssuerInfo{Name: "mock", Kind: "card_issuer", Sandbox: true}
}

func (m *MockCardIssuer) HealthCheck(ctx context.Context) error { return nil }

func (m *MockCardIssuer) IssueCard(ctx context.Context, req IssueCardRequest) (*IssuedCard, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.byKey[req.IdempotencyKey]; ok {
		return c, nil
	}
	m.seq++
	exp := time.Now().AddDate(3, 0, 0)
	providerCardID := "mkc_" + id.New().String()

	c := &IssuedCard{
		ProviderCardID: providerCardID,
		ProviderToken:  "tok_" + id.New().String(),
		Last4:          last4For(providerCardID),
		ExpiryMonth:    int(exp.Month()),
		ExpiryYear:     exp.Year(),
		Brand:          "visa",
	}
	m.byKey[req.IdempotencyKey] = c
	m.states[c.ProviderCardID] = "active"
	return c, nil
}

func (m *MockCardIssuer) SetCardState(ctx context.Context, providerCardID, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.states[providerCardID]; !ok && !strings.HasPrefix(providerCardID, "mkc_") {
		return &Error{Code: ErrInvalidRequest, Message: "unknown card"}
	}
	m.states[providerCardID] = state
	return nil
}

func (m *MockCardIssuer) RevealCard(ctx context.Context, providerCardID string) (*CardSecrets, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var issued *IssuedCard
	for _, c := range m.byKey {
		if c.ProviderCardID == providerCardID {
			issued = c
			break
		}
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(providerCardID))
	seed := h.Sum64()

	l4 := last4For(providerCardID)
	expMonth, expYear := 12, time.Now().Year()+3
	if issued != nil {
		expMonth, expYear = issued.ExpiryMonth, issued.ExpiryYear
	}

	// 4 (Visa) + ten digits + a free digit + the four we already show.
	digits := []byte(fmt.Sprintf("4%010d0%s", seed%10_000_000_000, l4))
	digits[11] = byte('0' + luhnFreeDigit(digits))

	return &CardSecrets{
		PAN:         string(digits),
		CVV:         fmt.Sprintf("%03d", seed%1000),
		ExpiryMonth: expMonth,
		ExpiryYear:  expYear,
	}, nil
}

func last4For(providerCardID string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte("last4:" + providerCardID))
	return fmt.Sprintf("%04d", h.Sum64()%10000)
}

func luhnFreeDigit(digits []byte) int {
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if i == 11 {
			d = 0
		} else if double {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return (10 - sum%10) % 10
}

// StateOf lets tests assert the issuer-side state.
func (m *MockCardIssuer) StateOf(providerCardID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[providerCardID]
}
