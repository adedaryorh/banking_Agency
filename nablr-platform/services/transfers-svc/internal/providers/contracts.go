// Package providers defines the payout rail interface and error types.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrorCode classifies provider errors for retry decisions.
type ErrorCode string

const (
	ErrTimeout         ErrorCode = "timeout"
	ErrRateLimited     ErrorCode = "rate_limited"
	ErrUnauthenticated ErrorCode = "unauthenticated"
	ErrInvalidRequest  ErrorCode = "invalid_request"

	ErrNotFound          ErrorCode = "not_found"
	ErrRejected          ErrorCode = "rejected"
	ErrInsufficientFunds ErrorCode = "insufficient_funds"
	ErrDuplicateRequest  ErrorCode = "duplicate_request"
	ErrUnavailable       ErrorCode = "unavailable"

	ErrNotSupported ErrorCode = "not_supported"
	ErrCircuitOpen  ErrorCode = "circuit_open"
	ErrUnknown      ErrorCode = "unknown"
)

type Error struct {
	Code        ErrorCode
	ProviderRef string
	Message     string // masked, safe for logs
	HTTPStatus  int
	Raw         json.RawMessage // masked
	Retryable   bool
}

func (e *Error) Error() string {
	return fmt.Sprintf("provider error [%s]: %s", e.Code, e.Message)
}

// AsError converts an error to *Error if it is one.
func AsError(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

type ProviderInfo struct {
	Name    string
	Version string
	Sandbox bool
}

// PayoutProvider is the interface every payout rail implements.
type PayoutProvider interface {
	Info() ProviderInfo
	HealthCheck(ctx context.Context) error
	SendPayout(ctx context.Context, req PayoutRequest) (*PayoutResult, error)
	PayoutStatus(ctx context.Context, reference string) (*PayoutStatusResult, error)
	ValidateAccount(ctx context.Context, countryCode, bankCode, accountNumber string) (*AccountValidation, error)
	// Reconcilable makes the provider's statement the counterparty record the
	// ledger reconciles against.
	Reconcilable
}

// PayoutRequest is a single payment instruction.
type PayoutRequest struct {
	IdempotencyKey string
	AmountMinor    int64
	Currency       string
	CountryCode    string
	AccountNumber  string
	BankCode       string
	BankName       string
	AccountName    string
	Narrative      string
}

// PayoutResult is the provider's acknowledgement.
type PayoutResult struct {
	ProviderRef string
	Status      PayoutStatus
}

// PayoutStatus reflects the rail's view of a payment.
type PayoutStatus string

const (
	PayoutPending    PayoutStatus = "pending"
	PayoutProcessing PayoutStatus = "processing"
	PayoutSettled    PayoutStatus = "settled"
	PayoutFailed     PayoutStatus = "failed"
	PayoutReturned   PayoutStatus = "returned"
)

// PayoutStatusResult is the rail's current state for a payment.
type PayoutStatusResult struct {
	ProviderRef string
	Status      PayoutStatus
	Raw         json.RawMessage
}

// AccountValidation is the result of a name enquiry.
type AccountValidation struct {
	AccountNumber string
	BankCode      string
	AccountName   string
	MatchResult   string // match | partial_match | no_match | not_supported
}

type StatementItem struct {
	// ProviderRef is the rail's handle for the payment.
	ProviderRef string
	// OurRef echoes the idempotency key we sent, when the provider returns it.
	OurRef      string
	AmountMinor int64
	Currency    string
	Status      string
	OccurredAt  time.Time
	Raw         json.RawMessage
}

type StatementPage struct {
	Items      []StatementItem
	NextCursor string
}

type Reconcilable interface {
	FetchStatement(ctx context.Context, from, to time.Time, cursor string) (*StatementPage, error)
}
