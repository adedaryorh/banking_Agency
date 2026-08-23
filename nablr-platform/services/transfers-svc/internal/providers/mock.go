package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type WebhookSink func(eventType string, payload json.RawMessage)

type MockProvider struct {
	mu          sync.Mutex
	payouts     map[string]*payoutRecord // by idempotency key
	byRef       map[string]*payoutRecord // by provider reference
	timeouts    map[string]bool          // keys that already timed out once
	errs        map[string]*Error        // injected errors by account number
	sink        WebhookSink
	settleDelay time.Duration
}

type payoutRecord struct {
	IdemKey     string
	ProviderRef string
	Amount      int64
	Currency    string
	Status      PayoutStatus
	CreatedAt   time.Time
	SettledAt   time.Time
}

func NewMockProvider() *MockProvider {
	return NewMockProviderWithSink(nil, 0)
}

// NewMockProviderWithSink builds the scriptable mock. settleDelay is the async
// settlement latency; zero picks a sane default.
func NewMockProviderWithSink(sink WebhookSink, settleDelay time.Duration) *MockProvider {
	if settleDelay <= 0 {
		settleDelay = 100 * time.Millisecond
	}
	return &MockProvider{
		payouts:     map[string]*payoutRecord{},
		byRef:       map[string]*payoutRecord{},
		timeouts:    map[string]bool{},
		errs:        map[string]*Error{},
		sink:        sink,
		settleDelay: settleDelay,
	}
}

func (m *MockProvider) Info() ProviderInfo {
	return ProviderInfo{Name: "mock", Version: "1.0"}
}

func (m *MockProvider) HealthCheck(ctx context.Context) error { return nil }

func (m *MockProvider) FetchStatement(ctx context.Context, from, to time.Time, cursor string) (*StatementPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	page := &StatementPage{}
	for _, rec := range m.byRef {
		if rec.CreatedAt.Before(from) || rec.CreatedAt.After(to) {
			continue
		}
		page.Items = append(page.Items, StatementItem{
			ProviderRef: rec.ProviderRef,
			OurRef:      rec.IdemKey,
			AmountMinor: rec.Amount,
			Currency:    rec.Currency,
			Status:      string(rec.Status),
			OccurredAt:  rec.CreatedAt,
		})
	}
	return page, nil
}

func (m *MockProvider) SendPayout(ctx context.Context, req PayoutRequest) (*PayoutResult, error) {
	if req.AmountMinor <= 0 {
		return nil, &Error{
			Code:      ErrInvalidRequest,
			Message:   "amount must be positive",
			Retryable: false,
		}
	}
	if req.AccountNumber == "" || req.BankCode == "" {
		return nil, &Error{
			Code:      ErrInvalidRequest,
			Message:   "missing required fields",
			Retryable: false,
		}
	}

	m.mu.Lock()

	if injected := m.errs[req.AccountNumber]; injected != nil {
		m.mu.Unlock()
		return nil, injected
	}

	// Idempotency: a resend with the same idempotency key returns the original
	// outcome, exactly as a real rail would.
	if existing, ok := m.payouts[req.IdempotencyKey]; ok {
		m.mu.Unlock()
		return &PayoutResult{ProviderRef: existing.ProviderRef, Status: existing.Status}, nil
	}

	switch suffix(req.AmountMinor) {
	case 13:
		m.mu.Unlock()
		return nil, &Error{
			Code: ErrRejected, Retryable: false,
			Message: "beneficiary bank rejected the credit",
		}
	case 99:
		if !m.timeouts[req.IdempotencyKey] {
			m.timeouts[req.IdempotencyKey] = true
			m.mu.Unlock()
			return nil, &Error{
				Code: ErrTimeout, Retryable: true,
				Message: "simulated gateway timeout",
			}
		}
		// The retry with the same key proceeds normally below.
	}

	rec := &payoutRecord{
		IdemKey:     req.IdempotencyKey,
		ProviderRef: "mk_" + uuid.New().String(),
		Amount:      req.AmountMinor,
		Currency:    req.Currency,
		Status:      PayoutProcessing,
		CreatedAt:   time.Now().UTC(),
	}
	m.payouts[req.IdempotencyKey] = rec
	m.byRef[rec.ProviderRef] = rec
	willFail := suffix(req.AmountMinor) == 21
	delay := m.settleDelay
	if suffix(req.AmountMinor) == 7 {
		delay = 2 * time.Second
	}
	sink := m.sink
	m.mu.Unlock()

	// Asynchronous settlement, exactly like a real scheme: the caller gets
	// "processing" now and the truth arrives later on a webhook.
	go m.settleLater(rec.ProviderRef, willFail, delay, sink)

	return &PayoutResult{
		ProviderRef: rec.ProviderRef,
		Status:      PayoutProcessing,
	}, nil
}

func (m *MockProvider) settleLater(providerRef string, fail bool, delay time.Duration, sink WebhookSink) {
	time.Sleep(delay)
	m.mu.Lock()
	rec, ok := m.byRef[providerRef]
	if !ok || rec.Status != PayoutProcessing {
		m.mu.Unlock()
		return
	}
	eventType := "payout.settled"
	if fail {
		rec.Status = PayoutFailed
		eventType = "payout.failed"
	} else {
		rec.Status = PayoutSettled
		rec.SettledAt = time.Now().UTC()
	}
	payload, _ := json.Marshal(map[string]any{
		"provider_ref": rec.ProviderRef,
		"our_ref":      rec.IdemKey,
		"amount":       rec.Amount,
		"currency":     rec.Currency,
		"status":       string(rec.Status),
		"failure_code": failureCode(fail),
	})
	m.mu.Unlock()

	if sink != nil {
		sink(eventType, payload)
	}
}

// suffix scripts mock behaviour by the last two digits of the minor amount.
func suffix(amountMinor int64) int64 {
	if amountMinor < 0 {
		amountMinor = -amountMinor
	}
	return amountMinor % 100
}

func failureCode(fail bool) string {
	if fail {
		return "RECIPIENT_ACCOUNT_BLOCKED"
	}
	return ""
}

func (m *MockProvider) PayoutStatus(ctx context.Context, reference string) (*PayoutStatusResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Look up by provider reference first, then by idempotency key — the
	// reconciliation paths consult by whatever the caller recorded.
	rec, ok := m.byRef[reference]
	if !ok {
		rec, ok = m.payouts[reference]
	}
	if !ok {
		return nil, &Error{
			Code:      ErrNotFound,
			Message:   "payout not found",
			Retryable: false,
		}
	}
	return &PayoutStatusResult{
		ProviderRef: rec.ProviderRef,
		Status:      rec.Status,
	}, nil
}

func (m *MockProvider) ValidateAccount(ctx context.Context, countryCode, bankCode, accountNumber string) (*AccountValidation, error) {
	if len(accountNumber) != 10 {
		return nil, &Error{
			Code:      ErrInvalidRequest,
			Message:   "account number must be 10 digits",
			Retryable: false,
		}
	}
	// Deterministic: account numbers ending 00 fail the name check.
	if accountNumber[len(accountNumber)-2:] == "00" {
		return &AccountValidation{
			AccountNumber: accountNumber,
			BankCode:      bankCode,
			MatchResult:   "no_match",
		}, nil
	}

	// Mock name matching logic
	accountName := fmt.Sprintf("Mock Account Holder %s", accountNumber[len(accountNumber)-4:])
	return &AccountValidation{
		AccountNumber: accountNumber,
		BankCode:      bankCode,
		AccountName:   accountName,
		MatchResult:   "match",
	}, nil
}

// SetPayoutStatus is a test helper to simulate webhook events
func (m *MockProvider) SetPayoutStatus(reference string, status PayoutStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.byRef[reference]; ok {
		rec.Status = status
	}
	if rec, ok := m.payouts[reference]; ok {
		rec.Status = status
	}
}

// SimulateSettlement marks a payout as settled (test helper)
func (m *MockProvider) SimulateSettlement(reference string) {
	m.SetPayoutStatus(reference, PayoutSettled)
}

// SimulateFailure marks a payout as failed (test helper)
func (m *MockProvider) SimulateFailure(reference string) {
	m.SetPayoutStatus(reference, PayoutFailed)
}

// GetAllPayouts returns all stored payouts (test helper)
func (m *MockProvider) GetAllPayouts() map[string]*PayoutStatusResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]*PayoutStatusResult, len(m.payouts)+len(m.byRef))
	for k, v := range m.payouts {
		result[k] = &PayoutStatusResult{ProviderRef: v.ProviderRef, Status: v.Status}
	}
	for k, v := range m.byRef {
		if _, dup := result[k]; !dup {
			result[k] = &PayoutStatusResult{ProviderRef: v.ProviderRef, Status: v.Status}
		}
	}
	return result
}

// Clear removes all stored payouts (test helper)
func (m *MockProvider) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.payouts = map[string]*payoutRecord{}
	m.byRef = map[string]*payoutRecord{}
	m.timeouts = map[string]bool{}
	m.errs = map[string]*Error{}
}

// InjectError simulates provider errors for testing
func (m *MockProvider) InjectError(accountNumber string, code ErrorCode, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errs[accountNumber] = &Error{
		Code:      codeForInjection(code),
		Message:   message,
		Retryable: false,
	}
}

// ShouldError checks if an error was injected for this account
func (m *MockProvider) ShouldError(accountNumber string) *Error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.errs[accountNumber]; ok {
		return e
	}
	return nil
}

// Reset clears all state and errors
func (m *MockProvider) Reset() {
	m.Clear()
}

func codeForInjection(code ErrorCode) ErrorCode {
	switch code {
	case ErrRejected, ErrInvalidRequest, ErrNotFound, ErrTimeout, ErrUnknown,
		ErrRateLimited, ErrUnauthenticated, ErrInsufficientFunds,
		ErrDuplicateRequest, ErrUnavailable, ErrNotSupported, ErrCircuitOpen:
		return code
	default:
		return ErrUnknown
	}
}

// ProviderCodeColumn helper for bank code translation
func ProviderCodeColumn(provider string) string {
	switch strings.ToLower(provider) {
	case "novac":
		return "novac_code"
	case "mock":
		return "" // mock uses standard codes
	default:
		return ""
	}
}
