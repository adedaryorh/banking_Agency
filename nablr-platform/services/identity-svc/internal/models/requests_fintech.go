package models

type VerifyPhoneRequest struct {
	PhoneNumber string `json:"phone_number" validate:"required,min=10,max=20"`
}

type ConfirmPhoneRequest struct {
	ChallengeToken string `json:"challenge_token" validate:"required"`
	Code           string `json:"code" validate:"required,min=4,max=8"`
}

type LookupPhoneDetailsRequest struct {
	PhoneNumber string `json:"phone_number" validate:"required,min=10,max=20"`
}

type VerifyBVNRequest struct {
	BVN                      string   `json:"bvn,omitempty" validate:"omitempty,len=11,numeric"`
	NIN                      string   `json:"nin,omitempty" validate:"omitempty,len=11,numeric"`
	ResultToken              string   `json:"result_token,omitempty" validate:"omitempty,max=4096"`
	EncryptedLivenessPayload string   `json:"encrypted_liveness_payload,omitempty" validate:"omitempty,max=65536"`
	SelfieImageBase64        string   `json:"selfie_image_base64" validate:"required,max=10485760"`
	LivenessFramesBase64     []string `json:"liveness_frame_images_base64" validate:"required,len=3,dive,required,max=10485760"`
}

type LookupBVNRequest struct {
	BVN string `json:"bvn" validate:"required,len=11,numeric"`
}

type LookupKYCDetailsRequest struct {
	PhoneNumber string `json:"phone_number,omitempty" validate:"omitempty,min=10,max=20"`
	BVN         string `json:"bvn,omitempty" validate:"omitempty,len=11,numeric"`
	NIN         string `json:"nin,omitempty" validate:"omitempty,len=11,numeric"`
}

type ConfirmBVNOTPRequest struct {
	ChallengeToken string `json:"challenge_token" validate:"required"`
	Code           string `json:"code" validate:"required,len=6,numeric"`
}

type VerifyNINRequest struct {
	NIN                      string   `json:"nin,omitempty" validate:"omitempty,len=11,numeric"`
	ResultToken              string   `json:"result_token,omitempty" validate:"omitempty,max=4096"`
	EncryptedLivenessPayload string   `json:"encrypted_liveness_payload,omitempty" validate:"omitempty,max=262144"`
	SelfieImageBase64        string   `json:"selfie_image_base64" validate:"required,max=10485760"`
	LivenessFramesBase64     []string `json:"liveness_frame_images_base64,omitempty" validate:"omitempty,dive,required,max=10485760"`
}

type FacialVerificationRequest struct {
	EncryptedLivenessPayload string   `json:"encrypted_liveness_payload,omitempty" validate:"omitempty,max=65536"`
	SelfieImageBase64        string   `json:"selfie_image_base64" validate:"required,max=10485760"`
	LivenessFramesBase64     []string `json:"liveness_frame_images_base64" validate:"required,len=3,dive,required,max=10485760"`
}

type LookupNINDetailsRequest struct {
	NIN string `json:"nin" validate:"required,len=11,numeric"`
}

type SubmitAddressRequest struct {
	AddressLine1 string `json:"address_line1" validate:"required,max=255,no_html"`
	AddressLine2 string `json:"address_line2" validate:"omitempty,max=255,no_html"`
	City         string `json:"city" validate:"required,max=100,no_html"`
	State        string `json:"state" validate:"required,max=100,no_html"`
	PostalCode   string `json:"postal_code" validate:"omitempty,max=20"`
}

type SetPINRequest struct {
	PIN string `json:"pin" validate:"required,min=4,max=6,numeric"`
}

type ChangePINRequest struct {
	CurrentPIN string `json:"current_pin" validate:"required,min=4,max=6,numeric"`
	NewPIN     string `json:"new_pin" validate:"required,min=4,max=6,numeric"`
}

type ResetPINRequest struct {
	ChallengeToken string `json:"challenge_token" validate:"required"`
	Code           string `json:"code" validate:"required,min=4,max=8"`
	NewPIN         string `json:"new_pin" validate:"required,min=4,max=6,numeric"`
}

type RequestOTPRequest struct {
	Purpose string `json:"purpose" validate:"required,oneof=phone_verification login new_device pin_reset pin_set high_value_transaction risk_challenge"`
	Channel string `json:"channel" validate:"omitempty,oneof=sms email"`
}

type VerifyOTPRequest struct {
	ChallengeToken string `json:"challenge_token" validate:"required"`
	Code           string `json:"code" validate:"required,min=4,max=8"`
}

type TrustDeviceRequest struct {
	DeviceID string `json:"device_id" validate:"required,uuid4"`
	Name     string `json:"name" validate:"omitempty,max=150,no_html"`
}

type InternalTransferRequest struct {
	FromAccountID   string `json:"from_account_id" validate:"required,uuid4"`
	ToAccountNumber string `json:"to_account_number" validate:"required,len=10"`
	AmountMinor     int64  `json:"amount_minor" validate:"required,min=1"`
	Narration       string `json:"narration" validate:"omitempty,max=255,no_html"`
	PIN             string `json:"pin" validate:"omitempty,min=4,max=6,numeric"`
}

type ResolveAccountRequest struct {
	BankCode      string `json:"bank_code" validate:"required,max=10"`
	AccountNumber string `json:"account_number" validate:"required,len=10,numeric"`
}

type ExternalTransferRequest struct {
	FromAccountID   string `json:"from_account_id" validate:"required,uuid4"`
	BankCode        string `json:"bank_code" validate:"required,max=10"`
	AccountNumber   string `json:"account_number" validate:"required,len=10,numeric"`
	AccountName     string `json:"account_name" validate:"omitempty,max=150,no_html"`
	AmountMinor     int64  `json:"amount_minor" validate:"required,min=1"`
	Narration       string `json:"narration" validate:"omitempty,max=255,no_html"`
	PIN             string `json:"pin" validate:"omitempty,min=4,max=6,numeric"`
	SaveBeneficiary bool   `json:"save_beneficiary"`
}

type PurchaseAirtimeRequest struct {
	AccountID   string `json:"account_id" validate:"required,uuid4"`
	Network     string `json:"network" validate:"required,max=40"`
	PhoneNumber string `json:"phone_number" validate:"required,min=10,max=20"`
	AmountMinor int64  `json:"amount_minor" validate:"required,min=5000"`
	PIN         string `json:"pin" validate:"omitempty,min=4,max=6,numeric"`
}

type PurchaseDataRequest struct {
	AccountID   string `json:"account_id" validate:"required,uuid4"`
	Network     string `json:"network" validate:"required,max=40"`
	PhoneNumber string `json:"phone_number" validate:"required,min=10,max=20"`
	BundleCode  string `json:"bundle_code" validate:"required,max=120"`
	AmountMinor int64  `json:"amount_minor" validate:"required,min=1"`
	PIN         string `json:"pin" validate:"omitempty,min=4,max=6,numeric"`
}

type VerifyMeterRequest struct {
	Disco       string `json:"disco" validate:"required,max=120"`
	MeterNumber string `json:"meter_number" validate:"required,max=40"`
	MeterType   string `json:"meter_type" validate:"required,oneof=prepaid postpaid"`
}

type PayElectricityRequest struct {
	AccountID   string `json:"account_id" validate:"required,uuid4"`
	Disco       string `json:"disco" validate:"required,max=120"`
	MeterNumber string `json:"meter_number" validate:"required,max=40"`
	MeterType   string `json:"meter_type" validate:"required,oneof=prepaid postpaid"`
	AmountMinor int64  `json:"amount_minor" validate:"required,min=1"`
	PhoneNumber string `json:"phone_number" validate:"omitempty,min=10,max=20"`
	PIN         string `json:"pin" validate:"omitempty,min=4,max=6,numeric"`
}

type VerifySmartcardRequest struct {
	Provider        string `json:"provider" validate:"required,max=120"`
	SmartcardNumber string `json:"smartcard_number" validate:"required,max=40"`
}

type PayCableRequest struct {
	AccountID       string `json:"account_id" validate:"required,uuid4"`
	Provider        string `json:"provider" validate:"required,max=120"`
	SmartcardNumber string `json:"smartcard_number" validate:"required,max=40"`
	PackageCode     string `json:"package_code" validate:"required,max=120"`
	AmountMinor     int64  `json:"amount_minor" validate:"required,min=1"`
	PhoneNumber     string `json:"phone_number" validate:"omitempty,min=10,max=20"`
	PIN             string `json:"pin" validate:"omitempty,min=4,max=6,numeric"`
}

type PayEducationRequest struct {
	AccountID     string `json:"account_id" validate:"required,uuid4"`
	BillerCode    string `json:"biller_code" validate:"required,max=120"`
	VariationCode string `json:"variation_code" validate:"required,max=120"`
	Quantity      int    `json:"quantity" validate:"required,min=1,max=10"`
	AmountMinor   int64  `json:"amount_minor" validate:"required,min=1"`
	PhoneNumber   string `json:"phone_number" validate:"omitempty,min=10,max=20"`
	PIN           string `json:"pin" validate:"omitempty,min=4,max=6,numeric"`
}

type CreateVaultRequest struct {
	AccountID string `json:"account_id" validate:"required,uuid4"`
	Name      string `json:"name" validate:"required,max=150,no_html"`
	Type      string `json:"type" validate:"required,oneof=flexible locked target"`

	InitialAmountMinor int64  `json:"initial_amount_minor" validate:"omitempty,min=0"`
	TargetAmountMinor  int64  `json:"target_amount_minor" validate:"omitempty,min=0"`
	MaturityDate       string `json:"maturity_date" validate:"omitempty,datetime=2006-01-02"`

	AutoDebitAmountMinor int64  `json:"auto_debit_amount_minor" validate:"omitempty,min=0"`
	AutoDebitCadence     string `json:"auto_debit_cadence" validate:"omitempty,oneof=daily weekly monthly"`
}

type VaultDepositRequest struct {
	AmountMinor int64  `json:"amount_minor" validate:"required,min=1"`
	PIN         string `json:"pin" validate:"omitempty,min=4,max=6,numeric"`
}

type VaultWithdrawRequest struct {
	AmountMinor       int64  `json:"amount_minor" validate:"required,min=1"`
	PIN               string `json:"pin" validate:"omitempty,min=4,max=6,numeric"`
	ConfirmEarlyBreak bool   `json:"confirm_early_break"`
}
