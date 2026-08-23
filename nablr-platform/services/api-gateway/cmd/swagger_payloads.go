package main

type SwaggerRegisterRequest struct {
	Email       string `json:"email" example:"user@example.com"`
	PhoneNumber string `json:"phone_number" example:"08093536368"`
	Password    string `json:"password" example:"Password123!"`
}

type SwaggerLoginRequest struct {
	Email       string `json:"email,omitempty" example:"user@example.com"`
	PhoneNumber string `json:"phone_number,omitempty" example:"08093536368"`
	Password    string `json:"password" example:"Password123!"`
}

type SwaggerTokenRequest struct {
	Token string `json:"token" example:"paste-token-here"`
}

type SwaggerEmailRequest struct {
	Email string `json:"email" example:"user@example.com"`
}

type SwaggerResetPasswordRequest struct {
	Token       string `json:"token" example:"paste-token-here"`
	NewPassword string `json:"new_password" example:"NewPassword123!"`
}

type SwaggerPhonePasswordResetRequest struct {
	PhoneNumber string `json:"phone_number" example:"07035141082"`
}

type SwaggerConfirmPhonePasswordResetRequest struct {
	ChallengeToken string `json:"challenge_token" example:"otp-challenge-token"`
	Code           string `json:"code" example:"123456"`
	NewPassword    string `json:"new_password" example:"123456"`
}

type SwaggerRefreshRequest struct {
	RefreshToken string `json:"refresh_token" example:"paste-refresh-token-here"`
}

type SwaggerPhoneOnboardingRegisterRequest struct {
	DeviceID       string                       `json:"deviceId" example:"device30"`
	PhoneNumber    string                       `json:"phone_number" example:"08093536368"`
	DeviceMetadata SwaggerDeviceMetadataRequest `json:"deviceMetadata"`
}

type SwaggerDeviceMetadataRequest struct {
	DeviceName string  `json:"deviceName" example:"MacOs"`
	Latitude   float64 `json:"latitude" example:"6.524379"`
	Longitude  float64 `json:"longitude" example:"3.379206"`
}

type SwaggerPhoneRequest struct {
	PhoneNumber string `json:"phone_number" example:"08093536368"`
}

type SwaggerOTPConfirmRequest struct {
	ChallengeToken string `json:"challenge_token" example:"otp-challenge-token"`
	Code           string `json:"code" example:"123456"`
}

type SwaggerBVNRequest struct {
	BVN string `json:"bvn" example:"12345678901"`
}

type SwaggerKYCDetailsRequest struct {
	PhoneNumber string `json:"phone_number,omitempty" example:"08093536368"`
	BVN         string `json:"bvn,omitempty" example:"12345678901"`
	NIN         string `json:"nin,omitempty" example:"12345678901"`
}

type SwaggerNINLivenessRequest struct {
	NIN                       string   `json:"nin" example:"12345678901"`
	EncryptedLivenessPayload  string   `json:"encrypted_liveness_payload,omitempty" example:"provider-payload"`
	SelfieImageBase64         string   `json:"selfie_image_base64" example:"REPLACE_WITH_BASE64_SELFIE"`
	LivenessFrameImagesBase64 []string `json:"liveness_frame_images_base64" example:"FRAME_1_BASE64,FRAME_2_BASE64,FRAME_3_BASE64"`
}

type SwaggerFacialVerificationRequest struct {
	EncryptedLivenessPayload  string   `json:"encrypted_liveness_payload,omitempty" example:"provider-payload"`
	SelfieImageBase64         string   `json:"selfie_image_base64" example:"REPLACE_WITH_BASE64_SELFIE"`
	LivenessFrameImagesBase64 []string `json:"liveness_frame_images_base64" example:"FRAME_1_BASE64,FRAME_2_BASE64,FRAME_3_BASE64"`
}

type SwaggerOnboardingDeviceRequest struct {
	DeviceID       string                       `json:"deviceId" example:"device30"`
	DeviceMetadata SwaggerDeviceMetadataRequest `json:"deviceMetadata"`
}

type SwaggerPasswordRequest struct {
	Password int `json:"password" example:"123456"`
}

type SwaggerMobileVersionRequest struct {
	Platform    string `json:"platform" example:"ios"`
	Version     string `json:"version" example:"1.0.0"`
	BuildNumber string `json:"build_number,omitempty" example:"100"`
}

type SwaggerProfileUpdateRequest struct {
	FirstName     string `json:"first_name,omitempty" example:"Abdulahi"`
	MiddleName    string `json:"middle_name,omitempty" example:"Ola"`
	LastName      string `json:"last_name,omitempty" example:"Adedayo"`
	DateOfBirth   string `json:"date_of_birth,omitempty" example:"1996-05-21"`
	Gender        string `json:"gender,omitempty" example:"male"`
	NablrUsername string `json:"nablr_username,omitempty" example:"adedayo"`
}

type SwaggerAddressRequest struct {
	AddressLine1 string `json:"address_line1" example:"12 Admiralty Way"`
	AddressLine2 string `json:"address_line2,omitempty" example:"Lekki Phase 1"`
	City         string `json:"city" example:"Lagos"`
	State        string `json:"state" example:"Lagos"`
	PostalCode   string `json:"postal_code,omitempty" example:"100001"`
}

type SwaggerPINRequest struct {
	PIN string `json:"pin" example:"1234"`
}

type SwaggerChangePINRequest struct {
	CurrentPIN string `json:"current_pin" example:"1234"`
	NewPIN     string `json:"new_pin" example:"5678"`
}

type SwaggerResetPINRequest struct {
	ChallengeToken string `json:"challenge_token" example:"otp-challenge-token"`
	Code           string `json:"code" example:"123456"`
	NewPIN         string `json:"new_pin" example:"5678"`
}

type SwaggerSecurityOTPRequest struct {
	Purpose string `json:"purpose" example:"pin_reset"`
	Channel string `json:"channel,omitempty" example:"sms"`
}

type SwaggerTrustDeviceRequest struct {
	DeviceID       string `json:"device_id" example:"00000000-0000-4000-8000-000000000001"`
	ChallengeToken string `json:"challenge_token" example:"otp-challenge-token"`
	Code           string `json:"code" example:"123456"`
}

type SwaggerSMSRequest struct {
	To   string `json:"to" example:"+2348093536368"`
	Body string `json:"body" example:"Your Nabla verification code is 123456"`
}

type SwaggerInternalEmailRequest struct {
	To      string `json:"to" example:"user@example.com"`
	Subject string `json:"subject" example:"Nabla test"`
	Text    string `json:"text,omitempty" example:"Email text body"`
	HTML    string `json:"html,omitempty" example:"<strong>Email HTML body</strong>"`
}

type SwaggerPushRequest struct {
	DeviceToken string            `json:"device_token" example:"firebase-device-token"`
	Title       string            `json:"title" example:"Nabla"`
	Body        string            `json:"body" example:"Push message body"`
	Data        map[string]string `json:"data,omitempty"`
}

type SwaggerScheduleAuthorizePINRequest struct {
	PIN                string `json:"pin" example:"1234"`
	Amount             string `json:"amount" example:"10000.00"`
	Currency           string `json:"currency" example:"NGN"`
	BeneficiaryID      string `json:"beneficiary_id,omitempty" example:"00000000-0000-4000-8000-000000000001"`
	RecipientType      string `json:"recipient_type,omitempty" example:"bank"`
	RecipientReference string `json:"recipient_reference,omitempty" example:"@adedayo"`
	BankCode           string `json:"bank_code,omitempty" example:"058"`
	AccountNumber      string `json:"account_number,omitempty" example:"0123456789"`
}

type SwaggerScheduledPaymentRequest struct {
	Name               string `json:"name" example:"Rent 2026 top-up"`
	BeneficiaryID      string `json:"beneficiary_id,omitempty" example:"00000000-0000-4000-8000-000000000001"`
	RecipientType      string `json:"recipient_type,omitempty" example:"bank"`
	RecipientReference string `json:"recipient_reference,omitempty" example:"@adedayo"`
	BankCode           string `json:"bank_code,omitempty" example:"058"`
	AccountNumber      string `json:"account_number,omitempty" example:"0123456789"`
	AccountName        string `json:"account_name,omitempty" example:"Aisha Sanni Muhammed"`
	Amount             string `json:"amount" example:"10000.00"`
	Currency           string `json:"currency" example:"NGN"`
	ScheduleType       string `json:"schedule_type" example:"recurring"`
	Frequency          string `json:"frequency,omitempty" example:"monthly"`
	PaymentDate        string `json:"payment_date" example:"2026-09-05"`
	EndDate            string `json:"end_date,omitempty" example:"2027-09-05"`
	MaxOccurrences     int32  `json:"max_occurrences,omitempty" example:"12"`
	Narrative          string `json:"narrative,omitempty" example:"Rent"`
	PINToken           string `json:"pin_token" example:"00000000-0000-4000-8000-000000000002"`
}

type SwaggerScheduledPaymentUpdateRequest struct {
	Name           string `json:"name,omitempty" example:"Updated rent"`
	Amount         string `json:"amount,omitempty" example:"12000.00"`
	Frequency      string `json:"frequency,omitempty" example:"monthly"`
	NextRunAt      string `json:"next_run_at,omitempty" example:"2026-10-05T09:00:00+01:00"`
	EndDate        string `json:"end_date,omitempty" example:"2027-10-05"`
	MaxOccurrences int32  `json:"max_occurrences,omitempty" example:"12"`
	Narrative      string `json:"narrative,omitempty" example:"Updated rent payment"`
}

type SwaggerPauseScheduleRequest struct {
	Reason string `json:"reason,omitempty" example:"Paused by customer"`
}
