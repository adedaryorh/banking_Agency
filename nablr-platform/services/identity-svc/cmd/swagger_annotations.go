package main

import "nabla/identity-svc/internal/models"

var _ = models.RegisterRequest{}

// @Summary Health check
// @Tags Health
// @Success 200 {object} map[string]interface{}
// @Router /health [get]
func swaggerIdentityHealth() {}

// @Summary Register with email, phone, and password
// @Tags Auth
// @Accept json
// @Produce json
// @Param X-Waitlist-Reservation-Token header string false "Confirmed waitlist reservation token used to claim the reserved Nablr username"
// @Param payload body models.RegisterRequest true "Registration payload"
// @Success 201 {object} map[string]interface{}
// @Router /api/v1/auth/register [post]
func swaggerIdentityAuthRegister() {}

// @Summary Login with email or phone and password
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.LoginRequest true "Login payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/login [post]
func swaggerIdentityAuthLogin() {}

// @Summary Verify email token
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.VerifyEmailRequest true "Email verification token"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/verify-email [post]
func swaggerIdentityVerifyEmail() {}

// @Summary Resend email verification
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.ResendVerificationRequest true "Email payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/resend-verification [post]
func swaggerIdentityResendVerification() {}

// @Summary Forgot password
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.ForgotPasswordRequest true "Forgot password payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/forgot-password [post]
func swaggerIdentityForgotPassword() {}

// @Summary Reset password
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.ResetPasswordRequest true "Reset password payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/reset-password [post]
func swaggerIdentityResetPassword() {}

// @Summary Request phone password reset OTP
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.PhonePasswordResetRequest true "Phone password reset request"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/password/phone/request [post]
func swaggerIdentityPhonePasswordResetRequest() {}

// @Summary Confirm phone password reset
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.ConfirmPhonePasswordResetRequest true "Phone password reset confirmation"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/password/phone/confirm [post]
func swaggerIdentityPhonePasswordResetConfirm() {}

// @Summary Refresh access token
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.RefreshTokenRequest true "Refresh token payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/refresh [post]
func swaggerIdentityRefresh() {}

// @Summary Logout
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.LogoutRequest true "Logout payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/logout [post]
func swaggerIdentityLogout() {}

// @Summary Request magic link
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.MagicLinkRequest true "Magic link request payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/magic-link/request [post]
func swaggerIdentityMagicLinkRequest() {}

// @Summary Confirm magic link
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body models.ConfirmMagicLinkRequest true "Magic link confirmation payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/magic-link/confirm [post]
func swaggerIdentityMagicLinkConfirm() {}

// @Summary Start phone onboarding
// @Tags Onboarding
// @Accept json
// @Produce json
// @Param X-Waitlist-Reservation-Token header string false "Confirmed waitlist reservation token used to claim the reserved Nablr username"
// @Param payload body models.PhoneOnboardingRegisterRequest true "Phone and device metadata payload"
// @Success 201 {object} map[string]interface{}
// @Router /api/v1/onboarding/register [post]
func swaggerIdentityOnboardingRegister() {}

// @Summary Resend onboarding OTP
// @Tags Onboarding
// @Security BearerAuth
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/otp/resend [post]
func swaggerIdentityOnboardingResendOTP() {}

// @Summary Refresh onboarding session
// @Tags Onboarding
// @Accept json
// @Produce json
// @Param X-Device-ID header string true "Client device ID"
// @Param payload body models.PhoneOnboardingRegisterRequest true "Phone payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/session/refresh [post]
func swaggerIdentityOnboardingRefreshSession() {}

// @Summary Confirm onboarding phone OTP
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param payload body models.PhoneOnboardingConfirmRequest true "OTP confirmation payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/phone/confirm [post]
func swaggerIdentityOnboardingConfirmPhone() {}

// @Summary Submit onboarding NIN liveness verification
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param X-Verification-Request-ID header string false "Optional provider verification request ID"
// @Param payload body models.VerifyNINRequest true "NIN liveness payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/nin [post]
func swaggerIdentityOnboardingNIN() {}

// @Summary Lookup onboarding phone, BVN, or NIN details
// @Description Uses the onboarding token instead of an access token. Send exactly one of phone_number, bvn, or nin.
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param payload body models.LookupKYCDetailsRequest true "Onboarding details lookup payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/details [post]
func swaggerIdentityOnboardingDetails() {}

// @Summary Submit onboarding facial verification
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param X-Verification-Request-ID header string false "Optional provider verification request ID"
// @Param payload body models.FacialVerificationRequest true "Facial verification payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/facial-verification [post]
func swaggerIdentityOnboardingFacial() {}

// @Summary Register onboarding device
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param payload body models.OnboardingDeviceRequest true "Device metadata payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/device [post]
func swaggerIdentityOnboardingDevice() {}

// @Summary Complete onboarding 6-digit passcode setup
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param payload body models.PhoneOnboardingPasswordRequest true "6-digit numeric passcode payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/password [post]
func swaggerIdentityOnboardingPassword() {}

// @Summary Get onboarding status
// @Tags Onboarding
// @Security BearerAuth
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/status [get]
func swaggerIdentityOnboardingStatus() {}

// @Summary Check mobile app version
// @Tags Mobile
// @Accept json
// @Produce json
// @Param payload body models.MobileVersionCheckRequest true "Version check payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/mobile/version/check [post]
func swaggerIdentityMobileVersionCheck() {}

// @Summary Get KYC profile
// @Tags KYC
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/profile [get]
func swaggerIdentityKYCProfile() {}

// @Summary List KYC attempts
// @Tags KYC
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/attempts [get]
func swaggerIdentityKYCAttempts() {}

// @Summary Get transaction limits
// @Tags KYC
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/limits [get]
func swaggerIdentityKYCLimits() {}

// @Summary Submit authenticated BVN verification
// @Tags KYC
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.LookupBVNRequest true "BVN payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/bvn [post]
func swaggerIdentityKYCBVN() {}

// @Summary Submit authenticated NIN verification
// @Tags KYC
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.LookupNINDetailsRequest true "NIN payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/nin [post]
func swaggerIdentityKYCNIN() {}

// @Summary Lookup phone, BVN, or NIN details
// @Description Calls the configured directory lookup provider. Send exactly one of phone_number, bvn, or nin. When nin is sent, the service returns the provider NIN lookup response.
// @Tags KYC
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.LookupKYCDetailsRequest true "KYC details lookup payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/details [post]
func swaggerIdentityKYCDetails() {}

// @Summary Submit address
// @Tags KYC
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.SubmitAddressRequest true "Address payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/address [post]
func swaggerIdentityKYCAddress() {}

// @Summary Get PIN status
// @Tags Security
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/pin [get]
func swaggerIdentityPINStatus() {}

// @Summary Set PIN
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.SetPINRequest true "PIN payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/pin [post]
func swaggerIdentitySetPIN() {}

// @Summary Change PIN
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.ChangePINRequest true "Change PIN payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/pin [patch]
func swaggerIdentityChangePIN() {}

// @Summary Reset PIN
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.ResetPINRequest true "Reset PIN payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/pin/reset [post]
func swaggerIdentityResetPIN() {}

// @Summary Request security OTP
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.RequestOTPRequest true "OTP request payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/otp/request [post]
func swaggerIdentitySecurityOTP() {}

// @Summary Verify security phone
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.VerifyPhoneRequest true "Phone verification payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/phone/verify [post]
func swaggerIdentityPhoneVerify() {}

// @Summary Confirm security phone
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.PhoneOnboardingConfirmRequest true "Phone confirmation payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/phone/confirm [post]
func swaggerIdentityPhoneConfirm() {}

// @Summary List user devices
// @Tags Security
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/devices [get]
func swaggerIdentityDevices() {}

// @Summary Trust device
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body models.TrustDeviceRequest true "Trust device payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/devices/trust [post]
func swaggerIdentityTrustDevice() {}

// @Summary Block device
// @Tags Security
// @Security BearerAuth
// @Produce json
// @Param deviceId path string true "Device ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/devices/{deviceId}/block [post]
func swaggerIdentityBlockDevice() {}
