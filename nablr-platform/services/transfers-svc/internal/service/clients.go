package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var ErrIdentityUnavailable = errors.New("identity service unavailable")

var ErrIdentityUserNotFound = errors.New("identity user not found")

type IdentityHealthCarrier interface {
	CircuitState() string
}

type IdentityClient interface {
	// VerifyPIN reports whether the supplied transaction PIN is correct for the

	VerifyPIN(ctx context.Context, userID uuid.UUID, pin string) (bool, error)
	// GetUser returns account-level facts: email, phone, role and status.
	GetUser(ctx context.Context, userID uuid.UUID) (UserProfile, error)

	GetKYCProfile(ctx context.Context, userID uuid.UUID) (KYCProfile, error)

	GetUserByUsername(ctx context.Context, username string) (UserProfile, error)
	// GetUserByAccountNumber resolves a live customer by their 10-digit Nablr
	// handle. Used by pay/resolve to turn "who are you paying?" digits into a
	// user. ErrIdentityUserNotFound when no match.
	GetUserByAccountNumber(ctx context.Context, accountNumber string) (UserProfile, error)
}

type Notifier interface {
	SendEmail(ctx context.Context, to, subject, text, html string) (bool, error)
}

// UserProfile is identity-svc's GetUser projected to what transfers-svc uses.
type UserProfile struct {
	UserID        string
	Email         string
	PhoneNumber   string
	Role          string
	Status        string
	PhoneVerified bool
	// NablrUsername is the @handle without the leading @.
	NablrUsername string

	AccountNumber string

	AvatarURL string
}

type KYCProfile struct {
	UserID      string
	Tier        int32
	FirstName   string
	MiddleName  string
	LastName    string
	PhoneNumber string
	PhoneStatus string
	BVNStatus   string
	NINStatus   string
	Sanctioned  bool
	PEP         bool
}
