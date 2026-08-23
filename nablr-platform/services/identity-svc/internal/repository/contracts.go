package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"nabla/identity-svc/internal/models"
)

var (
	ErrNotFound = errors.New("record not found")
	ErrConflict = errors.New("record already exists")
	ErrExpired  = errors.New("record expired or unavailable")
)

type Store interface {
	Atomic(context.Context, func(Store) error) error
	Auth() AuthStore
	KYC() KYCStore
	Security() SecurityStore
	Tier3() Tier3Store
	Outbox() OutboxStore
	Audit() AuditStore
	Waitlist() WaitlistStore
}

type AuthStore interface {
	UserByID(context.Context, uuid.UUID) (*models.User, error)
	UserByPhone(context.Context, string) (*models.User, error)
	// UserByUsername resolves a live user by their nablr_username (without the
	// leading @). Used by transfers-svc's pay/resolve to match any active
	// customer by handle.
	UserByUsername(context.Context, string) (*models.User, error)
	// UserByAccountNumber resolves a live user by their 10-digit Nablr handle.
	// Used by transfers-svc to turn "who are you paying?" digits into a user.
	UserByAccountNumber(context.Context, string) (*models.User, error)
	// UpdateUserAvatar stores the object-storage key of the user's profile image
	// on the user row and returns the refreshed user.
	UpdateUserAvatar(context.Context, uuid.UUID, string) (*models.User, error)
}

type KYCStore interface {
	ProfileByUserID(context.Context, uuid.UUID) (*models.KYCProfile, error)
	ProfileByBVNHash(context.Context, string) (*models.KYCProfile, error)
	ProfileByNINHash(context.Context, string) (*models.KYCProfile, error)
	CreateProfile(context.Context, *models.KYCProfile) error
	UpdateProfile(context.Context, *models.KYCProfile) error
	CreateAttempt(context.Context, *models.KYCVerificationAttempt) error
	AttemptsByUserID(context.Context, uuid.UUID, int) ([]models.KYCVerificationAttempt, error)
	CreateFacialCapture(context.Context, *models.KYCFacialCapture) error
	RecentAttemptCount(context.Context, uuid.UUID, string, time.Time) (int64, error)
	LimitForTier(context.Context, models.KYCTier, string) (*models.TransactionLimit, error)
	LockUsage(context.Context, uuid.UUID, string, string, string) (*models.LimitUsage, error)
	ApplyUsageDelta(context.Context, uuid.UUID, int64, int) error
	UsageForWindows(context.Context, uuid.UUID, string, map[string]string) (map[string]models.LimitUsage, error)
	UpsertLimit(context.Context, *models.TransactionLimit) error
	ListLimits(context.Context) ([]models.TransactionLimit, error)
}

type SecurityStore interface {
	PINByUserID(context.Context, uuid.UUID) (*models.TransactionPIN, error)
	LockPINByUserID(context.Context, uuid.UUID) (*models.TransactionPIN, error)
	CreatePIN(context.Context, *models.TransactionPIN) error
	UpdatePINHash(context.Context, uuid.UUID, string, time.Time) error
	RecordPINFailure(context.Context, uuid.UUID, *time.Time) error
	ResetPINAttempts(context.Context, uuid.UUID, time.Time) error
	RecentOTPCount(context.Context, uuid.UUID, models.OTPPurpose, time.Time) (int64, error)
	InvalidateOTPs(context.Context, uuid.UUID, models.OTPPurpose, time.Time) error
	CreateOTP(context.Context, *models.OTPCode) error
	OTPByChallengeToken(context.Context, string) (*models.OTPCode, error)
	RecordOTPFailure(context.Context, uuid.UUID) error
	ConsumeOTP(context.Context, uuid.UUID, time.Time) error
	DeviceByFingerprint(context.Context, uuid.UUID, string) (*models.UserDevice, error)
	DeviceByID(context.Context, uuid.UUID) (*models.UserDevice, error)
	TouchDevice(context.Context, uuid.UUID, string, time.Time) error
	UpsertDevice(context.Context, *models.UserDevice) error
	DevicesByUserID(context.Context, uuid.UUID) ([]models.UserDevice, error)
	SetDeviceTrusted(context.Context, uuid.UUID, bool, time.Time) error
	SetDeviceBlocked(context.Context, uuid.UUID, bool) error
	RecordAssessment(context.Context, *models.RiskAssessment) error
	AssessmentsByUserID(context.Context, uuid.UUID, int) ([]models.RiskAssessment, error)
}

type Tier3Store interface {
	ApplicationByUserID(context.Context, uuid.UUID) (*models.Tier3Application, error)
	CreateApplication(context.Context, *models.Tier3Application) error
	UpdateApplication(context.Context, *models.Tier3Application) error
	CreateDocument(context.Context, *models.Tier3Document) error
	DocumentByID(context.Context, uuid.UUID, uuid.UUID) (*models.Tier3Document, error)
}

type OutboxStore interface {
	Enqueue(context.Context, *models.OutboxEvent) error
	PendingCount(context.Context) (int64, error)
	ClaimBatch(context.Context, int, time.Time) ([]models.OutboxEvent, error)
	MarkPublished(context.Context, uuid.UUID, time.Time) error
	MarkFailed(context.Context, uuid.UUID, string, time.Time) error
	MarkDeadLetter(context.Context, uuid.UUID, string) error
	Replay(context.Context, uuid.UUID) error
	DeletePublishedBefore(context.Context, time.Time) (int64, error)
}

type AuditFilter struct {
	ActorID    *uuid.UUID
	EntityType string
	EntityID   *uuid.UUID
	Action     string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

type AuditStore interface {
	Record(context.Context, *models.AuditLog) error
	Search(context.Context, AuditFilter) ([]models.AuditLog, int64, error)
}

// WaitlistStore backs the public username-reservation and waitlist-join
// flow. Availability is enforced two ways: UsernameExists checks names
// already claimed by real accounts (users.nablr_username), while
// ActiveByUsername checks names currently held or confirmed by another
// waitlist entry. ExpireStaleByUsername lazily flips a stale hold or
// confirmation to 'expired' for one username right before a fresh
// check/reserve for that same name, so no background sweeper is needed.
type WaitlistStore interface {
	UsernameExists(context.Context, string) (bool, error)
	ExpireStaleByUsername(context.Context, string, time.Time) error
	ActiveByUsername(context.Context, string) (*models.WaitlistEntry, error)
	CreateWaitlistEntry(context.Context, *models.WaitlistEntry) error
	ByReservationTokenHash(context.Context, string) (*models.WaitlistEntry, error)
	ExpireWaitlistEntry(context.Context, uuid.UUID, time.Time) error
	ConfirmWaitlistEntry(context.Context, uuid.UUID, string, time.Time, time.Time) (*models.WaitlistEntry, error)
	ConfirmedWaitlistEntryByEmail(context.Context, string) (*models.WaitlistEntry, error)
}
