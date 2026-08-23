package main

// @Summary Gateway health check
// @Tags Gateway
// @Success 200 {object} map[string]interface{}
// @Router /health [get]
func swaggerGatewayHealth() {}

// @Summary Identity service health through gateway
// @Tags Gateway
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/identity/health [get]
func swaggerGatewayIdentityHealth() {}

// @Summary Notification service health through gateway
// @Tags Gateway
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/notification/health [get]
func swaggerGatewayNotificationHealth() {}

// @Summary Transfers service health through gateway
// @Tags Gateway
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/transfers/health [get]
func swaggerGatewayTransfersHealth() {}

// @Summary Register with email, phone, and password
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerRegisterRequest true "Registration payload"
// @Success 201 {object} map[string]interface{}
// @Router /api/v1/auth/register [post]
func swaggerGatewayAuthRegister() {}

// @Summary Login with email or phone and password
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerLoginRequest true "Login payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/login [post]
func swaggerGatewayAuthLogin() {}

// @Summary Verify email token
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerTokenRequest true "Email verification token"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/verify-email [post]
func swaggerGatewayVerifyEmail() {}

// @Summary Resend email verification
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerEmailRequest true "Email payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/resend-verification [post]
func swaggerGatewayResendVerification() {}

// @Summary Forgot password
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerEmailRequest true "Forgot password payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/forgot-password [post]
func swaggerGatewayForgotPassword() {}

// @Summary Reset password
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerResetPasswordRequest true "Reset password payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/reset-password [post]
func swaggerGatewayResetPassword() {}

// @Summary Request phone password reset OTP
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerPhonePasswordResetRequest true "Phone password reset request"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/password/phone/request [post]
func swaggerGatewayPhonePasswordResetRequest() {}

// @Summary Confirm phone password reset
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerConfirmPhonePasswordResetRequest true "Phone password reset confirmation"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/password/phone/confirm [post]
func swaggerGatewayPhonePasswordResetConfirm() {}

// @Summary Refresh access token
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerRefreshRequest true "Refresh token payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/refresh [post]
func swaggerGatewayRefresh() {}

// @Summary Logout
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerRefreshRequest true "Logout payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/logout [post]
func swaggerGatewayLogout() {}

// @Summary Request magic link
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerEmailRequest true "Magic link request payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/magic-link/request [post]
func swaggerGatewayMagicLinkRequest() {}

// @Summary Confirm magic link
// @Tags Auth
// @Accept json
// @Produce json
// @Param payload body SwaggerTokenRequest true "Magic link confirmation payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/auth/magic-link/confirm [post]
func swaggerGatewayMagicLinkConfirm() {}

// @Summary Start phone onboarding
// @Tags Onboarding
// @Accept json
// @Produce json
// @Param payload body SwaggerPhoneOnboardingRegisterRequest true "Phone and device metadata payload"
// @Success 201 {object} map[string]interface{}
// @Router /api/v1/onboarding/register [post]
func swaggerGatewayOnboardingRegister() {}

// @Summary Resend onboarding OTP
// @Tags Onboarding
// @Security BearerAuth
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/otp/resend [post]
func swaggerGatewayOnboardingResendOTP() {}

// @Summary Refresh onboarding session
// @Tags Onboarding
// @Accept json
// @Produce json
// @Param X-Device-ID header string true "Client device ID"
// @Param payload body SwaggerPhoneRequest true "Phone payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/session/refresh [post]
func swaggerGatewayOnboardingRefreshSession() {}

// @Summary Confirm onboarding phone OTP
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param payload body SwaggerOTPConfirmRequest true "OTP confirmation payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/phone/confirm [post]
func swaggerGatewayOnboardingConfirmPhone() {}

// @Summary Submit onboarding NIN liveness verification
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param X-Verification-Request-ID header string false "Optional provider verification request ID"
// @Param payload body SwaggerNINLivenessRequest true "NIN liveness payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/nin [post]
func swaggerGatewayOnboardingNIN() {}

// @Summary Lookup onboarding phone, BVN, or NIN details
// @Description Uses the onboarding token instead of an access token. Send exactly one of phone_number, bvn, or nin.
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param payload body SwaggerKYCDetailsRequest true "Onboarding details lookup payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/details [post]
func swaggerGatewayOnboardingDetails() {}

// @Summary Submit onboarding facial verification
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param X-Verification-Request-ID header string false "Optional provider verification request ID"
// @Param payload body SwaggerFacialVerificationRequest true "Facial verification payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/facial-verification [post]
func swaggerGatewayOnboardingFacial() {}

// @Summary Register onboarding device
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param payload body SwaggerOnboardingDeviceRequest true "Device metadata payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/device [post]
func swaggerGatewayOnboardingDevice() {}

// @Summary Complete onboarding 6-digit passcode setup
// @Tags Onboarding
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Param payload body SwaggerPasswordRequest true "6-digit numeric passcode payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/password [post]
func swaggerGatewayOnboardingPassword() {}

// @Summary Get onboarding status
// @Tags Onboarding
// @Security BearerAuth
// @Produce json
// @Param Authorization header string true "Bearer onboarding_token"
// @Param X-Device-ID header string true "Client device ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/onboarding/status [get]
func swaggerGatewayOnboardingStatus() {}

// @Summary Check mobile app version
// @Tags Mobile
// @Accept json
// @Produce json
// @Param payload body SwaggerMobileVersionRequest true "Version check payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/mobile/version/check [post]
func swaggerGatewayMobileVersionCheck() {}

// @Summary Get identity profile
// @Tags Identity
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/identity/profile [get]
func swaggerGatewayProfileGet() {}

// @Summary Update identity profile
// @Tags Identity
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerProfileUpdateRequest true "Profile payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/identity/profile [put]
func swaggerGatewayProfileUpdate() {}

// @Summary Get KYC profile
// @Tags KYC
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/profile [get]
func swaggerGatewayKYCProfile() {}

// @Summary List KYC attempts
// @Tags KYC
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/attempts [get]
func swaggerGatewayKYCAttempts() {}

// @Summary Get transaction limits
// @Tags KYC
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/limits [get]
func swaggerGatewayKYCLimits() {}

// @Summary Submit authenticated BVN verification
// @Tags KYC
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerBVNRequest true "BVN payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/bvn [post]
func swaggerGatewayKYCBVN() {}

// @Summary Submit authenticated NIN verification
// @Tags KYC
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerNINLivenessRequest true "NIN payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/nin [post]
func swaggerGatewayKYCNIN() {}

// @Summary Lookup phone, BVN, or NIN details
// @Description Proxies to identity service. Send exactly one of phone_number, bvn, or nin. When nin is sent, identity returns the provider NIN lookup response.
// @Tags KYC
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerKYCDetailsRequest true "KYC details lookup payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/details [post]
func swaggerGatewayKYCDetails() {}

// @Summary Submit address
// @Tags KYC
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerAddressRequest true "Address payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/kyc/address [post]
func swaggerGatewayKYCAddress() {}

// @Summary Get PIN status
// @Tags Security
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/pin [get]
func swaggerGatewayPINStatus() {}

// @Summary Set PIN
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerPINRequest true "PIN payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/pin [post]
func swaggerGatewaySetPIN() {}

// @Summary Change PIN
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerChangePINRequest true "Change PIN payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/pin [patch]
func swaggerGatewayChangePIN() {}

// @Summary Reset PIN
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerResetPINRequest true "Reset PIN payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/pin/reset [post]
func swaggerGatewayResetPIN() {}

// @Summary Request security OTP
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerSecurityOTPRequest true "OTP request payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/otp/request [post]
func swaggerGatewaySecurityOTP() {}

// @Summary Verify security phone
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerPhoneRequest true "Phone verification payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/phone/verify [post]
func swaggerGatewayPhoneVerify() {}

// @Summary Confirm security phone
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerOTPConfirmRequest true "Phone confirmation payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/phone/confirm [post]
func swaggerGatewayPhoneConfirm() {}

// @Summary List user devices
// @Tags Security
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/devices [get]
func swaggerGatewayDevices() {}

// @Summary Trust device
// @Tags Security
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerTrustDeviceRequest true "Trust device payload"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/devices/trust [post]
func swaggerGatewayTrustDevice() {}

// @Summary Block device
// @Tags Security
// @Security BearerAuth
// @Produce json
// @Param deviceId path string true "Device ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/security/devices/{deviceId}/block [post]
func swaggerGatewayBlockDevice() {}

// @Summary Send SMS through notification service
// @Tags Internal Notifications
// @Security InternalServiceToken
// @Accept json
// @Produce json
// @Param X-Internal-Service-Token header string true "Internal service token"
// @Param payload body SwaggerSMSRequest true "SMS payload"
// @Success 202 {object} map[string]interface{}
// @Router /api/v1/internal/v1/sms [post]
func swaggerGatewaySendSMS() {}

// @Summary Send email through notification service
// @Tags Internal Notifications
// @Security InternalServiceToken
// @Accept json
// @Produce json
// @Param X-Internal-Service-Token header string true "Internal service token"
// @Param payload body SwaggerInternalEmailRequest true "Email payload"
// @Success 202 {object} map[string]interface{}
// @Router /api/v1/internal/v1/email [post]
func swaggerGatewaySendEmail() {}

// @Summary Send push through notification service
// @Tags Internal Notifications
// @Security InternalServiceToken
// @Accept json
// @Produce json
// @Param X-Internal-Service-Token header string true "Internal service token"
// @Param payload body SwaggerPushRequest true "Push payload"
// @Success 202 {object} map[string]interface{}
// @Router /api/v1/internal/v1/push [post]
func swaggerGatewaySendPush() {}

// @Summary Authorize scheduled payment PIN
// @Tags Scheduled Payments
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerScheduleAuthorizePINRequest true "PIN and exact scheduled-payment details"
// @Success 201 {object} map[string]interface{}
// @Router /api/v1/scheduled-payments/authorize-pin [post]
func swaggerGatewayScheduleAuthorizePIN() {}

// @Summary Create a scheduled payment
// @Tags Scheduled Payments
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param payload body SwaggerScheduledPaymentRequest true "Scheduled payment"
// @Success 201 {object} map[string]interface{}
// @Router /api/v1/scheduled-payments [post]
func swaggerGatewayCreateScheduledPayment() {}

// @Summary List scheduled payments
// @Tags Scheduled Payments
// @Security BearerAuth
// @Produce json
// @Param status query string false "active, paused, completed, cancelled, or failed"
// @Param limit query int false "Page size (1-100)"
// @Param offset query int false "Page offset"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/scheduled-payments [get]
func swaggerGatewayListScheduledPayments() {}

// @Summary Get scheduled payment details
// @Tags Scheduled Payments
// @Security BearerAuth
// @Produce json
// @Param id path string true "Scheduled payment ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/scheduled-payments/{id} [get]
func swaggerGatewayGetScheduledPayment() {}

// @Summary Update an active scheduled payment
// @Tags Scheduled Payments
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Scheduled payment ID"
// @Param payload body SwaggerScheduledPaymentUpdateRequest true "Fields to update"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/scheduled-payments/{id} [patch]
func swaggerGatewayUpdateScheduledPayment() {}

// @Summary Pause a scheduled payment
// @Tags Scheduled Payments
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Scheduled payment ID"
// @Param payload body SwaggerPauseScheduleRequest false "Pause reason"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/scheduled-payments/{id}/pause [post]
func swaggerGatewayPauseScheduledPayment() {}

// @Summary Resume a scheduled payment
// @Tags Scheduled Payments
// @Security BearerAuth
// @Produce json
// @Param id path string true "Scheduled payment ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/scheduled-payments/{id}/resume [post]
func swaggerGatewayResumeScheduledPayment() {}

// @Summary Delete a scheduled payment
// @Tags Scheduled Payments
// @Security BearerAuth
// @Produce json
// @Param id path string true "Scheduled payment ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/scheduled-payments/{id}/cancel [post]
func swaggerGatewayCancelScheduledPayment() {}

// @Summary Get scheduled payment run history
// @Tags Scheduled Payments
// @Security BearerAuth
// @Produce json
// @Param id path string true "Scheduled payment ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/scheduled-payments/{id}/runs [get]
func swaggerGatewayScheduledPaymentRuns() {}
