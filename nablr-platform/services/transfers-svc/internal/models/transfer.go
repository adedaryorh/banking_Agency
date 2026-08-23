package models

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrTransferNotFound    = errors.New("transfers: not found")
	ErrBeneficiaryNotFound = errors.New("transfers: beneficiary not found")
	// ErrBankUnsupported: the payout rail has no code for this bank, so the
	// payment is refused before any money moves rather than after.
	ErrBankUnsupported      = errors.New("transfers: bank not supported by the payout rail")
	ErrBeneficiaryCooling   = errors.New("transfers: beneficiary in cooling period")
	ErrBeneficiaryBlocked   = errors.New("transfers: beneficiary blocked")
	ErrBeneficiaryDuplicate = errors.New("transfers: beneficiary already exists")
	ErrInvalidTransition    = errors.New("transfers: invalid state transition")
	ErrNotCancellable       = errors.New("transfers: transfer can no longer be cancelled")
	ErrSameWallet           = errors.New("transfers: source and destination are the same wallet")
	ErrCurrencyMismatch     = errors.New("transfers: currency mismatch")
	ErrSelfBeneficiary      = errors.New("transfers: cannot add yourself as a beneficiary")
	ErrUnsupportedCorridor  = errors.New("transfers: corridor not supported")
	ErrQuoteExpired         = errors.New("transfers: quote expired")
	ErrQuoteNotFound        = errors.New("transfers: quote not found")
	// ErrBeneficiaryMismatch: the body named a beneficiary the quote was not
	// priced for. The quote is authoritative and would silently overwrite the
	// body's beneficiary_id, so a caller who sent a *different* one is confused
	// about who is being paid — refuse rather than pay the quote's payee under
	// the wrong intent. Omitting beneficiary_id in the body is still allowed.
	ErrBeneficiaryMismatch    = errors.New("transfers: beneficiary does not match the quote")
	ErrLimitExceeded          = errors.New("transfers: limit exceeded")
	ErrPaymentsFrozen         = errors.New("transfers: payments are restricted on this account")
	ErrZeroAmount             = errors.New("transfers: amount must be positive")
	ErrPaymentRequestNotFound = errors.New("transfers: payment request not found")
	ErrRequestNotPending      = errors.New("transfers: payment request is no longer pending")
	ErrRequestExpired         = errors.New("transfers: payment request has expired")
	ErrNotRequestPayer        = errors.New("transfers: only the payer may act on this request")
	ErrNotRequestRequester    = errors.New("transfers: only the requester may cancel this request")
	// ErrNotRequestParty: the caller is neither the requester nor the payer, so
	// they may not even read it.
	ErrNotRequestParty = errors.New("transfers: not a party to this payment request")
	// ErrSelfPaymentRequest: you cannot request money from yourself (the DB CHECK
	// is the backstop; this is the friendly, early refusal).
	ErrSelfPaymentRequest = errors.New("transfers: cannot request money from yourself")

	ErrPINRequired = errors.New("transfers: transaction PIN is required")

	ErrPINInvalid = errors.New("transfers: transaction PIN is incorrect")

	ErrPINNotSet = errors.New("transfers: transaction PIN is not set")

	ErrPINLocked = errors.New("transfers: transaction PIN is locked")

	ErrPINAuthInvalid   = errors.New("transfers: PIN authorization is invalid or expired")
	ErrAccountNotActive = errors.New("transfers: account is not active")

	ErrSanctioned = errors.New("transfers: account is restricted")

	ErrAuthorizationUnavailable = errors.New("transfers: authorization service unavailable")
	ErrScheduleNotFound         = errors.New("transfers: scheduled payment not found")
)

type ScheduleValidationError struct {
	Code    string
	Field   string
	Message string
}

func (e *ScheduleValidationError) Error() string { return e.Message }

type Status string

const (
	StatusCreated     Status = "created"
	StatusPending     Status = "pending"
	StatusProcessing  Status = "processing"
	StatusUnderReview Status = "under_review"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusReversed    Status = "reversed"
	StatusRefunded    Status = "refunded"
	StatusCancelled   Status = "cancelled"
)

var transitions = map[Status][]Status{
	StatusCreated:     {StatusPending, StatusCancelled, StatusFailed},
	StatusPending:     {StatusProcessing, StatusUnderReview, StatusCancelled, StatusFailed},
	StatusUnderReview: {StatusProcessing, StatusCompleted, StatusFailed, StatusCancelled},
	StatusProcessing:  {StatusCompleted, StatusUnderReview, StatusFailed},
	StatusCompleted:   {StatusReversed, StatusRefunded},
	StatusFailed:      {},
	StatusCancelled:   {},
	StatusReversed:    {},
	StatusRefunded:    {},
}

// CanTransition reports whether from → to is legal.
func CanTransition(from, to Status) bool {
	for _, allowed := range transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// IsTerminal reports whether a status admits no further transitions.
func IsTerminal(s Status) bool { return len(transitions[s]) == 0 }

type TransferType string

const (
	TypeInternal      TransferType = "internal"
	TypeLocalBank     TransferType = "local_bank"
	TypeInternational TransferType = "international"
	TypeRemittance    TransferType = "remittance"
)

// Transfer is the customer-initiated payment.
type Transfer struct {
	ID                  uuid.UUID
	Reference           string
	CustomerID          uuid.UUID
	InitiatedByUserID   uuid.UUID
	SourceWalletID      uuid.UUID
	BeneficiaryID       uuid.UUID
	DestinationWalletID uuid.UUID
	Type                TransferType

	SendAmountMinor    int64
	SendCurrency       string
	ReceiveAmountMinor int64
	ReceiveCurrency    string
	FeeMinor           int64
	FeeCurrency        string
	TotalDebitMinor    int64

	FXQuoteID uuid.UUID

	Status        Status
	FailureCode   string
	FailureReason string

	HoldID                uuid.UUID
	LedgerTransactionID   uuid.UUID
	ReversalTransactionID uuid.UUID

	ProviderName      string
	ProviderReference string

	Narrative string

	IdempotencyKey string
	CreatedAt      time.Time

	SubmittedAt *time.Time
	CompletedAt *time.Time
}

// Beneficiary is a saved payee.
type Beneficiary struct {
	ID          uuid.UUID
	CustomerID  uuid.UUID
	Type        string // internal_user | bank_account
	DisplayName string
	Nickname    string

	BeneficiaryCustomerID uuid.UUID // internal

	CountryCode        string
	Currency           string
	BankCode           string
	BankName           string
	AccountName        string
	AccountNumberLast4 string
	// Decrypted only transiently for provider calls; never serialised.
	AccountNumberPlaintext string `json:"-"`

	VerificationStatus string
	VerifiedName       string

	CoolingPeriodEndsAt *time.Time
	IsFavourite         bool
	// IsSaved marks one of YOUR payees, as opposed to the record of a
	// one-off payment. Both are real rows — the transfer references one and
	// the receipt reads from it — but only saved ones belong in the list.
	IsSaved    bool
	Status     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

func (b *Beneficiary) InCoolingPeriod(now time.Time) bool {
	return b.CoolingPeriodEndsAt != nil && now.Before(*b.CoolingPeriodEndsAt)
}

var newPayeeAllowance = map[string]int64{
	"NGN": 2_000_000, // ₦20,000.00
	"GBP": 5_000,     // £50.00
	"USD": 5_000,     // $50.00
	"EUR": 5_000,     // €50.00
}

var ErrTierLimitExceeded = errors.New("transfers: verification tier limit exceeded")

var ErrTierLimitsNotSet = errors.New("transfers: no verification limits on record")

func NewPayeeAllowance(currency string) int64 {
	if v, ok := newPayeeAllowance[currency]; ok {
		return v
	}
	return 5_000
}

type Quote struct {
	ID                 uuid.UUID
	CustomerID         uuid.UUID
	SourceWalletID     uuid.UUID
	BeneficiaryID      uuid.UUID
	Type               TransferType
	SendAmountMinor    int64
	SendCurrency       string
	ReceiveAmountMinor int64
	ReceiveCurrency    string
	FeeMinor           int64
	FeeCurrency        string
	TotalDebitMinor    int64
	FXQuoteID          uuid.UUID
	RateNumerator      int64
	RateScale          int16
	ExpiresAt          time.Time
	CreatedAt          time.Time
	HighValue          bool
	HighValueNote      string
}

type Schedule struct {
	ID            uuid.UUID
	BeneficiaryID uuid.UUID
	PayeeName     string
	Type          string
	Frequency     string
	AmountMinor   int64
	Currency      string
	Narrative     string
	NextRunAt     time.Time
	LastRunAt     *time.Time
	Status        string
	Occurrences   int
}

// IsTerminal returns true if the status is final (cannot transition further)
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusReversed, StatusRefunded, StatusCancelled:
		return true
	default:
		return false
	}
}
