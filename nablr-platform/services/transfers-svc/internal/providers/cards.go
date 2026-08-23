package providers

import "context"

type CardIssuerInfo struct {
	Name         string
	Kind         string
	Sandbox      bool
	Capabilities []string
}

const CapabilityCardholderKYC = "cardholder_kyc"

// IssueCardRequest asks the issuer for a new tokenised card.
type IssueCardRequest struct {
	IdempotencyKey string
	CardType       string // virtual | physical | disposable
	Currency       string
	NameOnCard     string
	Cardholder     Cardholder
}

// Cardholder is the identity an issuer needs before it will issue.
type Cardholder struct {
	ProviderCustomerID string

	FirstName   string
	LastName    string
	Phone       string
	Email       string
	DateOfBirth string
	// IdentityType is the national identifier being presented: NIN or BVN.
	IdentityType   string
	IdentityNumber string
	Address        CardholderAddress
}

type CardholderAddress struct {
	Line1      string
	City       string
	State      string
	PostalCode string
	Country    string
}

// IssuedCard is what the issuer hands back.
type IssuedCard struct {
	ProviderCardID string
	// ProviderCustomerID is the cardholder the issuer created or reused, kept
	// so the next card for this person does not make a second one.
	ProviderCustomerID string
	ProviderToken      string
	Last4              string
	ExpiryMonth        int
	ExpiryYear         int
	Brand              string
}

type CardSecrets struct {
	PAN         string
	CVV         string
	ExpiryMonth int
	ExpiryYear  int
	NameOnCard  string
}

// CardIssuer is the interface every card rail implements.
type CardIssuer interface {
	Info() CardIssuerInfo
	HealthCheck(ctx context.Context) error
	IssueCard(ctx context.Context, req IssueCardRequest) (*IssuedCard, error)
	// SetCardState pushes freeze/unfreeze/terminate to the issuer.
	SetCardState(ctx context.Context, providerCardID, state string) error
	// RevealCard fetches the card's numbers for display. Callers must have
	// completed step-up authentication first.
	RevealCard(ctx context.Context, providerCardID string) (*CardSecrets, error)
}

// IssuerNeedsCardholderKYC reports whether the issuer will refuse a card
// without an identity of its own for the cardholder.
func IssuerNeedsCardholderKYC(issuer CardIssuer) bool {
	if issuer == nil {
		return false
	}
	for _, c := range issuer.Info().Capabilities {
		if c == CapabilityCardholderKYC {
			return true
		}
	}
	return false
}
