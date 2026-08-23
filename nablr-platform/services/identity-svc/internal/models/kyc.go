package models

import (
	"time"

	"encoding/json"

	"github.com/google/uuid"
)

type KYCTier int

const (
	KYCTier0 KYCTier = 0
	KYCTier1 KYCTier = 1
	KYCTier2 KYCTier = 2
	KYCTier3 KYCTier = 3
)

type KYCVerificationStatus string

const (
	KYCStatusUnverified   KYCVerificationStatus = "unverified"
	KYCStatusPending      KYCVerificationStatus = "pending"
	KYCStatusVerified     KYCVerificationStatus = "verified"
	KYCStatusRejected     KYCVerificationStatus = "rejected"
	KYCStatusManualReview KYCVerificationStatus = "manual_review"
)

type KYCProfile struct {
	ID     uuid.UUID `json:"id"`
	UserID uuid.UUID `json:"user_id"`
	Tier   KYCTier   `json:"tier"`

	FirstName   string     `json:"first_name,omitempty"`
	MiddleName  string     `json:"middle_name,omitempty"`
	LastName    string     `json:"last_name,omitempty"`
	DateOfBirth *time.Time `json:"date_of_birth,omitempty"`
	Gender      string     `json:"gender,omitempty"`
	PhoneNumber string     `json:"phone_number,omitempty"`
	//accountNumber  string     `json:"phone_number,omitempty"`

	PhoneStatus     KYCVerificationStatus `json:"phone_status"`
	PhoneVerifiedAt *time.Time            `json:"phone_verified_at,omitempty"`

	BVNEncrypted        string                `json:"-"`
	BVNLast4            string                `json:"bvn_last4,omitempty"`
	BVNHash             string                `json:"-"`
	BVNStatus           KYCVerificationStatus `json:"bvn_status"`
	BVNVerifiedAt       *time.Time            `json:"bvn_verified_at,omitempty"`
	BVNPhoneVerifiedAt  *time.Time            `json:"bvn_phone_verified_at,omitempty"`
	BVNProviderRef      string                `json:"bvn_provider_ref,omitempty"`
	BVNSelfieStatus     KYCVerificationStatus `json:"bvn_selfie_status"`
	BVNLivenessScore    float64               `json:"bvn_liveness_score"`
	BVNFaceMatchScore   float64               `json:"bvn_face_match_score"`
	BVNSelfieVerifiedAt *time.Time            `json:"bvn_selfie_verified_at,omitempty"`

	NINEncrypted  string                `json:"-"`
	NINLast4      string                `json:"nin_last4,omitempty"`
	NINHash       string                `json:"-"`
	NINStatus     KYCVerificationStatus `json:"nin_status"`
	NINVerifiedAt *time.Time            `json:"nin_verified_at,omitempty"`

	AddressLine1  string                `json:"address_line1,omitempty"`
	AddressLine2  string                `json:"address_line2,omitempty"`
	City          string                `json:"city,omitempty"`
	State         string                `json:"state,omitempty"`
	PostalCode    string                `json:"postal_code,omitempty"`
	Country       string                `json:"country"`
	AddressStatus KYCVerificationStatus `json:"address_status"`

	Sanctioned   bool   `json:"sanctioned"`
	PEP          bool   `json:"pep"`
	RejectReason string `json:"reject_reason,omitempty"`

	ProviderPayload json.RawMessage `json:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (KYCProfile) TableName() string { return "kyc_profiles" }

type KYCVerificationAttempt struct {
	ID            uuid.UUID             `json:"id"`
	UserID        uuid.UUID             `json:"user_id"`
	Kind          string                `json:"kind"`
	Provider      string                `json:"provider"`
	Status        KYCVerificationStatus `json:"status"`
	ProviderRef   string                `json:"provider_ref,omitempty"`
	MatchScore    int                   `json:"match_score"`
	FailureReason string                `json:"failure_reason,omitempty"`
	ResponseBody  json.RawMessage       `json:"-"`
	CreatedAt     time.Time             `json:"created_at"`
}

func (KYCVerificationAttempt) TableName() string { return "kyc_verification_attempts" }

type KYCFacialCapture struct {
	ID                    uuid.UUID       `json:"id"`
	UserID                uuid.UUID       `json:"user_id"`
	AttemptID             uuid.UUID       `json:"attempt_id"`
	VerificationRequestID string          `json:"verification_request_id,omitempty"`
	SelfieImage           []byte          `json:"-"`
	LivenessFrameImages   json.RawMessage `json:"-"`
	CreatedAt             time.Time       `json:"created_at"`
}

func (KYCFacialCapture) TableName() string { return "kyc_facial_captures" }

type TransactionLimit struct {
	ID       uuid.UUID `json:"id"`
	Tier     KYCTier   `json:"tier"`
	Currency string    `json:"currency"`

	SingleTransactionMaxMinor int64 `json:"single_transaction_max_minor"`
	DailyMaxMinor             int64 `json:"daily_max_minor"`
	MonthlyMaxMinor           int64 `json:"monthly_max_minor"`
	MaxBalanceMinor           int64 `json:"max_balance_minor"`

	DailyCountMax int `json:"daily_count_max"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (TransactionLimit) TableName() string { return "transaction_limits" }

type LimitUsage struct {
	ID         uuid.UUID `json:"id"`
	UserID     uuid.UUID `json:"user_id"`
	Currency   string    `json:"currency"`
	WindowType string    `json:"window_type"`
	WindowKey  string    `json:"window_key"`
	UsedMinor  int64     `json:"used_minor"`
	UsedCount  int       `json:"used_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (LimitUsage) TableName() string { return "limit_usage" }

const (
	LimitWindowDaily   = "daily"
	LimitWindowMonthly = "monthly"
)
