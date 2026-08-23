package providers

import (
	"context"
	"time"
)

type VirtualAccount struct {
	// ProviderRef is the vendor's handle for the account.
	ProviderRef   string
	AccountNumber string
	AccountName   string
	BankName      string
	BankCode      string
	Permanent     bool
}

// VirtualAccountRequest asks the rail to provision one.
type VirtualAccountRequest struct {
	Reference   string
	AccountName string
	FirstName   string
	LastName    string
	Email       string
	Currency    string
}

// CollectionStatus is the rail's view of one inbound payment.
type CollectionStatus string

const (
	CollectionPending CollectionStatus = "pending"
	CollectionSettled CollectionStatus = "settled"
	CollectionFailed  CollectionStatus = "failed"
)

// Collection is one payment that arrived into a virtual account.
type Collection struct {
	ProviderRef string
	// AccountNumber is the virtual account that was credited — this is how a
	// collection is matched to a customer. Everything else in the payload is
	// description.
	AccountNumber string
	AmountMinor   int64
	FeeMinor      int64
	Currency      string
	Status        CollectionStatus
	SenderName    string
	Narrative     string
	OccurredAt    time.Time
}

type CollectionProvider interface {
	Info() ProviderInfo
	// CreateVirtualAccount provisions a customer's funding account. Idempotent
	// on Reference.
	CreateVirtualAccount(ctx context.Context, req VirtualAccountRequest) (*VirtualAccount, error)
	// GetVirtualAccount reads one back by its account number.
	GetVirtualAccount(ctx context.Context, accountNumber string) (*VirtualAccount, error)
	GetCollection(ctx context.Context, providerRef string) (*Collection, error)
}
