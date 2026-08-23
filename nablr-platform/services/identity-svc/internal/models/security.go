package models

import (
	"time"

	"github.com/google/uuid"
)

type TransactionPIN struct {
	ID             uuid.UUID  `json:"id"`
	UserID         uuid.UUID  `json:"user_id"`
	PINHash        string     `json:"-"`
	FailedAttempts int        `json:"failed_attempts"`
	LockedUntil    *time.Time `json:"locked_until,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	LastChangedAt  time.Time  `json:"last_changed_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func (TransactionPIN) TableName() string { return "transaction_pins" }

func (p TransactionPIN) IsLocked(now time.Time) bool {
	return p.LockedUntil != nil && p.LockedUntil.After(now)
}

type OTPPurpose string

const (
	OTPPurposePhoneVerification OTPPurpose = "phone_verification"
	OTPPurposeBVNVerification   OTPPurpose = "bvn_verification"
	OTPPurposeLogin             OTPPurpose = "login"
	OTPPurposeNewDevice         OTPPurpose = "new_device"
	OTPPurposePINReset          OTPPurpose = "pin_reset"
	OTPPurposePINSet            OTPPurpose = "pin_set"
	OTPPurposeHighValueTxn      OTPPurpose = "high_value_transaction"
	OTPPurposeRiskChallenge     OTPPurpose = "risk_challenge"
)

type OTPChannel string

const (
	OTPChannelSMS   OTPChannel = "sms"
	OTPChannelEmail OTPChannel = "email"
)

type OTPCode struct {
	ID             uuid.UUID  `json:"id"`
	UserID         uuid.UUID  `json:"user_id"`
	Purpose        OTPPurpose `json:"purpose"`
	Channel        OTPChannel `json:"channel"`
	Destination    string     `json:"-"`
	CodeHash       string     `json:"-"`
	ChallengeToken string     `json:"-"`
	FailedAttempts int        `json:"failed_attempts"`
	ConsumedAt     *time.Time `json:"consumed_at,omitempty"`
	ExpiresAt      time.Time  `json:"expires_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

func (OTPCode) TableName() string { return "otp_codes" }

type UserDevice struct {
	ID          uuid.UUID  `json:"id"`
	UserID      uuid.UUID  `json:"user_id"`
	Fingerprint string     `json:"fingerprint"`
	Name        string     `json:"name,omitempty"`
	Platform    string     `json:"platform,omitempty"`
	PushToken   string     `json:"-"`
	AppVersion  string     `json:"app_version,omitempty"`
	OSVersion   string     `json:"os_version,omitempty"`
	DeviceModel string     `json:"device_model,omitempty"`
	Trusted     bool       `json:"trusted"`
	Blocked     bool       `json:"blocked"`
	LastIP      string     `json:"last_ip,omitempty"`
	Latitude    *float64   `json:"latitude,omitempty"`
	Longitude   *float64   `json:"longitude,omitempty"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	TrustedAt   *time.Time `json:"trusted_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (UserDevice) TableName() string { return "user_devices" }

type RiskDecision string

const (
	RiskDecisionAllow     RiskDecision = "allow"
	RiskDecisionChallenge RiskDecision = "challenge"
	RiskDecisionBlock     RiskDecision = "block"
)

type RiskAssessment struct {
	ID        uuid.UUID    `json:"id"`
	UserID    uuid.UUID    `json:"user_id"`
	Operation string       `json:"operation"`
	Decision  RiskDecision `json:"decision"`
	Score     int          `json:"score"`

	TriggeredRules string `json:"triggered_rules,omitempty"`

	AmountMinor int64      `json:"amount_minor"`
	Currency    string     `json:"currency,omitempty"`
	DeviceID    *uuid.UUID `json:"device_id,omitempty"`
	IPAddress   string     `json:"ip_address,omitempty"`
	Reference   string     `json:"reference,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (RiskAssessment) TableName() string { return "risk_assessments" }
