package models

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrAccountNotFound = errors.New("wallet: account not found")
	ErrWalletNotFound  = errors.New("wallet: wallet not found")
	ErrWalletExists    = errors.New("wallet: wallet already exists for this currency")
	ErrNotOwner        = errors.New("wallet: caller does not own this wallet")
	ErrWalletClosed    = errors.New("wallet: wallet closed")
)

// Account groups a customer's wallets
type Account struct {
	ID         uuid.UUID
	CustomerID uuid.UUID
	Type       string
	Name       string
	Reference  string
	Status     string
	IsPrimary  bool
	OpenedAt   time.Time
}

// Wallet is one currency purse backed 1:1 by a ledger account
type Wallet struct {
	ID              uuid.UUID
	AccountID       uuid.UUID
	CustomerID      uuid.UUID
	LedgerAccountID uuid.UUID
	Currency        string
	Name            string
	Type            string
	IsDefault       bool
	Status          string
	CreatedAt       time.Time

	// Balances are read through the ledger account at query time
	BalanceMinor   int64
	ReservedMinor  int64
	PendingMinor   int64
	AvailableMinor int64
}

// TypeSpend is one kind of outgoing, with its share of the month (port of
// usenablr-1.0 modules/wallet/app/insights.go).
type TypeSpend struct {
	Type        string
	AmountMinor int64
	SharePct    int
	Count       int
}

// Commitment is a scheduled payment that has not run yet.
type Commitment struct {
	ID          uuid.UUID
	Name        string
	AmountMinor int64
	DueAt       time.Time
	Frequency   string
}

// BiggestSpend is the single largest outgoing of the month.
type BiggestSpend struct {
	Description string
	AmountMinor int64
	OccurredAt  time.Time
}

// WalletInsights is the month in numbers, plus what is already spoken for.
type WalletInsights struct {
	Currency         string
	MonthIn          int64
	MonthOut         int64
	LastMonthOut     int64
	OutChangePct     *int
	DaysElapsed      int
	DaysInMonth      int
	ProjectedOut     int64
	DailyOut         int64
	Biggest          *BiggestSpend
	ByType           []TypeSpend
	Commitments      []Commitment
	CommittedMinor   int64
	SafeToSpendMinor int64
	AvailableMinor   int64
	OnHoldMinor      int64
	PendingCount     int
	GivingMinor      int64
}

// FeedItem is one row of the customer-facing transaction feed
type FeedItem struct {
	Status              string
	ID                  uuid.UUID
	WalletID            uuid.UUID
	LedgerTransactionID uuid.UUID
	Direction           string
	AmountMinor         int64
	Currency            string
	FeeMinor            int64
	BalanceAfterMinor   *int64
	TransactionType     string
	CounterpartyName    string
	CounterpartyType    string
	Description         string
	Category            string
	Note                string
	IsPending           bool
	OccurredAt          time.Time
	SourceType          string
	SourceID            uuid.UUID
	Reference           string
}
