package models

type PhoneOnboardingRegisterRequest struct {
	PhoneNumber    string                `json:"phone_number" validate:"required,min=10,max=20" example:"08093536368"`
	DeviceID       string                `json:"deviceId,omitempty" validate:"omitempty,max=255" example:"device30"`
	DeviceMetadata DeviceMetadataRequest `json:"deviceMetadata,omitempty"`
}
type DeviceMetadataRequest struct {
	DeviceName string   `json:"deviceName,omitempty" validate:"omitempty,max=150,no_html" example:"MacOs"`
	Latitude   *float64 `json:"latitude,omitempty" validate:"omitempty,min=-90,max=90" example:"6.524379"`
	Longitude  *float64 `json:"longitude,omitempty" validate:"omitempty,min=-180,max=180" example:"3.379206"`
}

type OnboardingDeviceRequest struct {
	DeviceID       string                `json:"deviceId" validate:"required,max=255" example:"device30"`
	DeviceMetadata DeviceMetadataRequest `json:"deviceMetadata,omitempty"`
}

type MobileVersionCheckRequest struct {
	Platform    string `json:"platform" validate:"required,max=40"`
	Version     string `json:"version" validate:"required,max=40"`
	BuildNumber string `json:"build_number,omitempty" validate:"omitempty,max=40"`
}

type PhoneOnboardingConfirmRequest struct {
	ChallengeToken string `json:"challenge_token" validate:"required"`
	Code           string `json:"code" validate:"required,len=6,numeric"`
}

type PhoneOnboardingPasswordRequest struct {
	Password string `json:"password" validate:"required,len=6,numeric" example:"123456"`
}

type PhoneOnboardingTokenRequest struct {
	OnboardingToken string `json:"onboarding_token" validate:"required"`
}

type RegisterRequest struct {
	Email       string `json:"email" validate:"required,email"`
	PhoneNumber string `json:"phone_number" validate:"required,min=10,max=20"`
	Password    string `json:"password" validate:"required,password"`
}

type LoginRequest struct {
	Email       string `json:"email,omitempty" validate:"omitempty,email"`
	PhoneNumber string `json:"phone_number,omitempty" validate:"omitempty,min=10,max=20"`
	Password    string `json:"password" validate:"required"`
}

type GoogleAuthRequest struct {
	IDToken     string `json:"id_token" validate:"required"`
	PhoneNumber string `json:"phone_number,omitempty" validate:"omitempty,min=10,max=20"`
}

type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

type LogoutRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

type VerifyEmailRequest struct {
	Token string `json:"token" validate:"required"`
}

type ResendVerificationRequest struct {
	Email string `json:"email" validate:"required,email"`
}

type ForgotPasswordRequest struct {
	Email       string `json:"email" validate:"omitempty,email"`
	PhoneNumber string `json:"phone_number" validate:"omitempty,min=10,max=20"`
}

type ResetPasswordRequest struct {
	Token          string `json:"token" validate:"omitempty"`
	ChallengeToken string `json:"challenge_token" validate:"omitempty"`
	Code           string `json:"code" validate:"omitempty,len=6,numeric"`
	NewPassword    string `json:"new_password" validate:"required,min=4,max=72"`
}

type PhonePasswordResetRequest struct {
	PhoneNumber string `json:"phone_number" validate:"required,min=10,max=20"`
}

type ConfirmPhonePasswordResetRequest struct {
	ChallengeToken string `json:"challenge_token" validate:"required"`
	Code           string `json:"code" validate:"required,len=6,numeric"`
	NewPassword    string `json:"new_password" validate:"required,min=4,max=6,numeric"`
}

type MagicLinkRequest struct {
	Email string `json:"email" validate:"required,email"`
}

type ConfirmMagicLinkRequest struct {
	Token string `json:"token" validate:"required"`
}

// RequestAccountClosureRequest is step 1 of the account closure flow: "Tell
// us why". ReasonNote is only meaningful (and shown by the app) when Reason
// is AccountClosureReasonOthers, but it's accepted alongside any reason.
type RequestAccountClosureRequest struct {
	Reason     AccountClosureReason `json:"reason" validate:"required"`
	ReasonNote string               `json:"reason_note,omitempty" validate:"omitempty,max=500,no_html"`
}

// ConfirmAccountClosureRequest is step 3: the code the app displayed after
// facial re-verification passed, typed back in by the user.
type ConfirmAccountClosureRequest struct {
	RequestID string `json:"request_id" validate:"required,uuid4"`
	Code      string `json:"code" validate:"required,len=6,numeric"`
}

// RestoreAccountRequest redeems the restore token Login issues when it
// rejects a deactivated account, without requiring an access token.
type RestoreAccountRequest struct {
	Token string `json:"token" validate:"required"`
}

type CreateAccountRequest struct {
	Currency string `json:"currency" validate:"required,oneof=NGN USD GBP"`
}

type TransferRequest struct {
	FromAccountID   string `json:"from_account_id" validate:"required,uuid4"`
	ToAccountNumber string `json:"to_account_number" validate:"required,len=10"`
	AmountMinor     int64  `json:"amount_minor" validate:"required,min=1"`
	Reference       string `json:"reference" validate:"required"`
}

type AddMoneyRequest struct {
	AccountID   string `json:"account_id" validate:"required,uuid4"`
	AmountMinor int64  `json:"amount_minor" validate:"required,min=1"`
	Reference   string `json:"reference" validate:"required"`
}

type GetAccountByNumberRequest struct {
	AccountNumber string `json:"account_number" validate:"required,len=10"`
}

type WaitlistReserveUsernameRequest struct {
	Username string `json:"username" validate:"required,nablr_username"`
}

type WaitlistJoinRequest struct {
	ReservationToken string `json:"reservation_token" validate:"required"`
	Email            string `json:"email" validate:"required,email"`
}
