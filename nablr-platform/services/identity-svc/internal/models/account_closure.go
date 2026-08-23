package models

import (
	"time"

	"github.com/google/uuid"
)

// AccountClosureReason is the set of reasons the app's "Tell us why" screen
// offers. ReasonOthers is the only one paired with a free-text note.
type AccountClosureReason string

const (
	AccountClosureReasonDifferentBank   AccountClosureReason = "moved_to_different_bank"
	AccountClosureReasonAnotherAccount  AccountClosureReason = "has_another_nablr_account"
	AccountClosureReasonAppIssues       AccountClosureReason = "app_issues_or_poor_experience"
	AccountClosureReasonPrivacyConcerns AccountClosureReason = "privacy_concerns"
	AccountClosureReasonSwitchingBanks  AccountClosureReason = "switching_banks"
	AccountClosureReasonAccountTheft    AccountClosureReason = "worried_about_account_theft"
	AccountClosureReasonPoorService     AccountClosureReason = "poor_customer_service"
	AccountClosureReasonOthers          AccountClosureReason = "others"
)

// ValidAccountClosureReasons lists every accepted value, in the order the
// app presents them, for request validation.
var ValidAccountClosureReasons = []AccountClosureReason{
	AccountClosureReasonDifferentBank,
	AccountClosureReasonAnotherAccount,
	AccountClosureReasonAppIssues,
	AccountClosureReasonPrivacyConcerns,
	AccountClosureReasonSwitchingBanks,
	AccountClosureReasonAccountTheft,
	AccountClosureReasonPoorService,
	AccountClosureReasonOthers,
}

type AccountClosureStatus string

const (
	// AccountClosureStatusPendingVerification is set when the reason has been
	// captured but facial re-verification hasn't happened yet.
	AccountClosureStatusPendingVerification AccountClosureStatus = "pending_verification"
	// AccountClosureStatusPendingConfirmation is set once facial verification
	// passes and a confirmation code has been issued, awaiting the user to
	// type it back.
	AccountClosureStatusPendingConfirmation AccountClosureStatus = "pending_confirmation"
	// AccountClosureStatusConfirmed is set once the code is confirmed and the
	// account has actually been deactivated.
	AccountClosureStatusConfirmed AccountClosureStatus = "confirmed"
	AccountClosureStatusCancelled AccountClosureStatus = "cancelled"
)

// AccountClosureRequest is the audit trail for one attempt to close an
// account: why, whether the face check passed, and whether the confirmation
// code was ever redeemed. It does not itself carry the account's live
// status — that stays on users.status, flipped by the same SoftDeleteUser
// path the old bare deactivate endpoint used.
type AccountClosureRequest struct {
	ID               uuid.UUID            `json:"id"`
	UserID           uuid.UUID            `json:"user_id"`
	Reason           AccountClosureReason `json:"reason"`
	ReasonNote       string               `json:"reason_note,omitempty"`
	Status           AccountClosureStatus `json:"status"`
	FacialVerifiedAt *time.Time           `json:"facial_verified_at,omitempty"`
	CodeHash         string               `json:"-"`
	CodeExpiresAt    *time.Time           `json:"code_expires_at,omitempty"`
	CodeAttempts     int                  `json:"-"`
	ConfirmedAt      *time.Time           `json:"confirmed_at,omitempty"`
	RestoredAt       *time.Time           `json:"restored_at,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

func (AccountClosureRequest) TableName() string { return "account_closure_requests" }

// IsValidAccountClosureReason reports whether reason is one of the accepted
// values.
func IsValidAccountClosureReason(reason AccountClosureReason) bool {
	for _, valid := range ValidAccountClosureReasons {
		if reason == valid {
			return true
		}
	}
	return false
}
