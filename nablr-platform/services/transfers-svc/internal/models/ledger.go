package models

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrLedgerAccountNotFound = errors.New("ledger: account not found")
	ErrInsufficientFunds     = errors.New("ledger: insufficient available balance")
	ErrHoldNotFound          = errors.New("ledger: hold not found")
	ErrHoldNotActive         = errors.New("ledger: hold not active")
	ErrHoldExpired           = errors.New("ledger: hold expired")
	ErrInvalidAmount         = errors.New("ledger: invalid amount")
	ErrLedgerZeroAmount      = errors.New("ledger: amount must be positive")
	ErrUnbalancedTransaction = errors.New("ledger: transaction must balance")
)

// AccountType classifies ledger accounts
type AccountType string

const (
	AccountCustomerAsset    AccountType = "customer_asset"
	AccountProviderClearing AccountType = "provider_clearing"
	AccountFees             AccountType = "fees"
	AccountRevenue          AccountType = "revenue"
	// AccountAdjustment is the platform-owned counterpart for a manual admin
	// credit/debit — the same shape as AccountProviderClearing, but for
	// money movements that didn't come through a payment rail.
	AccountAdjustment AccountType = "platform_adjustment"
)

// HoldType classifies what reserved the funds
type HoldType string

const (
	HoldTransfer HoldType = "transfer"
	HoldPayment  HoldType = "payment"
	HoldReserve  HoldType = "reserve"
	// HoldCardAuthorisation is money reserved by a card authorisation and
	// released or captured when the issuer says what actually happened.
	HoldCardAuthorisation HoldType = "card_authorisation"
)

// HoldStatus tracks hold lifecycle
type HoldStatus string

const (
	HoldActive   HoldStatus = "active"
	HoldCaptured HoldStatus = "captured"
	HoldReleased HoldStatus = "released"
	HoldExpired  HoldStatus = "expired"
)

// TransactionKind categorizes ledger movements
type TransactionKind string

const (
	KindInternalTransfer TransactionKind = "internal_transfer"
	KindExternalTransfer TransactionKind = "external_transfer"
	KindRemittance       TransactionKind = "remittance"
	KindDeposit          TransactionKind = "deposit"
	KindWithdrawal       TransactionKind = "withdrawal"
	// KindCardSettlement is a card payment clearing: the customer's account is
	// debited and the issuer's clearing account credited.
	KindCardSettlement TransactionKind = "card_settlement"
	// KindCardRefund reverses a settlement the merchant or a chargeback gave back.
	KindCardRefund TransactionKind = "card_refund"
	// KindFee is what the platform charges, e.g. the cost of issuing a card.
	KindFee TransactionKind = "fee"
	// KindSavingsIn is a round-up sweep into a savings balance (one tidy line
	// per sweep instead of kobo noise on every card payment).
	KindSavingsIn TransactionKind = "savings_in"
	// KindAdjustment is a manual admin credit/debit to a customer's wallet —
	// support corrections, promo credits — outside any payment rail.
	KindAdjustment TransactionKind = "admin_adjustment"
)

// Direction is debit or credit
type Direction string

const (
	Debit  Direction = "debit"
	Credit Direction = "credit"
)

// LedgerAccount is a double-entry ledger account
type LedgerAccount struct {
	ID             uuid.UUID
	Type           AccountType
	Currency       string
	OwnerType      string
	CustomerID     uuid.UUID
	ProviderName   string
	Name           string
	Code           string
	BalanceMinor   int64
	ReservedMinor  int64
	PendingMinor   int64
	AvailableMinor int64
	Status         string
	CreatedAt      time.Time
}

// Hold reserves funds on an account
type Hold struct {
	ID          uuid.UUID
	AccountID   uuid.UUID
	Reference   string
	AmountMinor int64
	Currency    string
	HoldType    HoldType
	Status      HoldStatus
	SourceType  string
	SourceID    uuid.UUID
	ExpiresAt   time.Time
	CreatedAt   time.Time
	CapturedAt  *time.Time
	ReleasedAt  *time.Time
}

// Transaction is a balanced set of entries
type Transaction struct {
	ID          uuid.UUID
	Reference   string
	Kind        TransactionKind
	Description string
	Status      string
	PostedAt    time.Time
	Entries     []Entry
}

// Entry is one line in a transaction
type Entry struct {
	ID                uuid.UUID
	TransactionID     uuid.UUID
	AccountID         uuid.UUID
	Direction         Direction
	AmountMinor       int64
	Currency          string
	BalanceAfterMinor int64
	Description       string
	CreatedAt         time.Time
}
