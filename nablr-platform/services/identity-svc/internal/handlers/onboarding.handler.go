package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/controllers"
	"nabla/identity-svc/internal/models"
	"nabla/identity-svc/internal/providers"
)

type OnboardingHandler struct {
	auth     controllers.AuthController
	otp      controllers.OTPController
	kyc      controllers.KYCController
	security controllers.SecurityController
}

func NewOnboardingHandler(auth controllers.AuthController, otp controllers.OTPController, kyc controllers.KYCController, security controllers.SecurityController) *OnboardingHandler {
	return &OnboardingHandler{auth: auth, otp: otp, kyc: kyc, security: security}
}

func (h *OnboardingHandler) Register(c *gin.Context) {
	var request models.PhoneOnboardingRegisterRequest
	if !bindAndValidate(c, &request) {
		return
	}
	user, token, err := h.auth.StartPhoneOnboarding(c.Request.Context(), request.PhoneNumber, c.GetHeader("X-Waitlist-Reservation-Token"))
	if err != nil {
		authFailure(c, err)
		return
	}
	var device *models.UserDevice
	var deviceCreated bool
	if h.security != nil {
		if input, ok := onboardingDeviceInput(c, request); ok {
			device, deviceCreated, err = h.security.RegisterDevice(c.Request.Context(), user.ID, input)
			if err != nil {
				log.Printf("onboarding device registration failed user_id=%s device_id=%q error=%v", user.ID, input.Fingerprint, err)
				device = nil
			}
		}
	}
	challenge, err := h.issueOTP(c, user.ID, user.PhoneNumber)
	if err != nil {
		securityFailure(c, err)
		return
	}
	response := gin.H{"user_id": user.ID, "phone_number": user.PhoneNumber, "nablr_username": user.NablrUsername, "onboarding_token": token, "otp": challenge}
	if device != nil {
		response["device"] = device
		response["device_created"] = deviceCreated
	}
	helpers.Success(c, http.StatusCreated, messages.OnboardingStarted, response)
}

func (h *OnboardingHandler) ResendOTP(c *gin.Context) {
	if !requireDeviceIDHeader(c) {
		return
	}
	token, ok := onboardingToken(c)
	if !ok {
		return
	}
	user, err := h.auth.OnboardingUser(c.Request.Context(), token)
	if err != nil {
		authFailure(c, err)
		return
	}
	challenge, err := h.issueOTP(c, user.ID, user.PhoneNumber)
	if err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.VerificationCodeSent, challenge)
}

func (h *OnboardingHandler) RefreshSession(c *gin.Context) {
	var request models.PhoneOnboardingRegisterRequest
	if !bindAndValidate(c, &request) {
		return
	}
	if firstNonEmpty(request.DeviceID, deviceIDHeader(c)) == "" {
		deviceIDRequired(c)
		return
	}
	user, token, err := h.auth.RefreshPhoneOnboardingSession(c.Request.Context(), request.PhoneNumber)
	if err != nil {
		authFailure(c, err)
		return
	}
	response := gin.H{"user_id": user.ID, "phone_number": user.PhoneNumber, "onboarding_token": token, "phone_verified": user.PhoneVerified}
	if !user.PhoneVerified {
		challenge, err := h.issueOTP(c, user.ID, user.PhoneNumber)
		if err != nil {
			securityFailure(c, err)
			return
		}
		response["otp"] = challenge
	}
	helpers.Success(c, http.StatusOK, messages.OnboardingSessionRefreshed, response)
}

func (h *OnboardingHandler) ConfirmPhone(c *gin.Context) {
	if !requireDeviceIDHeader(c) {
		return
	}
	var request models.PhoneOnboardingConfirmRequest
	if !bindAndValidate(c, &request) {
		return
	}
	token, ok := onboardingToken(c)
	if !ok {
		return
	}
	user, err := h.auth.OnboardingUser(c.Request.Context(), token)
	if err != nil {
		authFailure(c, err)
		return
	}
	if _, err = h.otp.Verify(c.Request.Context(), nil, request.ChallengeToken, request.Code, models.OTPPurposePhoneVerification); err != nil {
		securityFailure(c, err)
		return
	}
	if _, err = h.kyc.MarkPhoneVerified(c.Request.Context(), user.ID, user.PhoneNumber); err != nil {
		kycFailure(c, err)
		return
	}
	if _, err = h.auth.MarkOnboardingPhoneVerified(c.Request.Context(), user.ID); err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.PhoneVerified, nil)
}

func (h *OnboardingHandler) SubmitNIN(c *gin.Context) {
	var request models.VerifyNINRequest
	if !bindAndValidate(c, &request) {
		return
	}
	h.submitNIN(c, request)
}

func (h *OnboardingHandler) Details(c *gin.Context) {
	if !requireDeviceIDHeader(c) {
		return
	}
	token, ok := onboardingToken(c)
	if !ok {
		return
	}
	user, err := h.auth.OnboardingUser(c.Request.Context(), token)
	if err != nil {
		authFailure(c, err)
		return
	}
	var request models.LookupKYCDetailsRequest
	if !bindAndValidate(c, &request) {
		return
	}
	selected := 0
	if strings.TrimSpace(request.PhoneNumber) != "" {
		selected++
	}
	if strings.TrimSpace(request.BVN) != "" {
		selected++
	}
	if strings.TrimSpace(request.NIN) != "" {
		selected++
	}
	if selected != 1 {
		helpers.Failure(c, http.StatusBadRequest, messages.ErrIdentityNumberRequired.Error(), gin.H{"code": "single_identity_number_required"})
		return
	}
	result, err := h.kyc.LookupDetails(c.Request.Context(), user.ID, request.PhoneNumber, request.BVN, request.NIN)
	if err != nil {
		kycFailure(c, err)
		return
	}
	profile, _ := h.kyc.Profile(c.Request.Context(), user.ID)
	helpers.Success(c, http.StatusOK, messages.KYCDetailsRetrieved, gin.H{
		"lookup":  result,
		"journey": onboardingJourney(user, profile),
	})
}

func (h *OnboardingHandler) SubmitBVN(c *gin.Context) {
	var request models.VerifyBVNRequest
	if !bindAndValidate(c, &request) {
		return
	}
	hasBVN := strings.TrimSpace(request.BVN) != ""
	hasNIN := strings.TrimSpace(request.NIN) != ""
	if hasBVN && hasNIN {
		helpers.Failure(c, http.StatusBadRequest, "send either bvn or nin, not both", gin.H{"code": "single_identity_number_required"})
		return
	}
	if hasNIN {
		h.submitNIN(c, models.VerifyNINRequest{
			NIN:                      request.NIN,
			ResultToken:              request.ResultToken,
			EncryptedLivenessPayload: request.EncryptedLivenessPayload,
			SelfieImageBase64:        request.SelfieImageBase64,
			LivenessFramesBase64:     request.LivenessFramesBase64,
		})
		return
	}
	if !hasBVN {
		helpers.Failure(c, http.StatusBadRequest, "bvn or nin is required", gin.H{"code": "identity_number_required"})
		return
	}
	h.submitBVN(c, request)
}

func (h *OnboardingHandler) submitNIN(c *gin.Context, request models.VerifyNINRequest) {
	if !requireDeviceIDHeader(c) {
		return
	}
	verificationRequestID := verificationRequestID(c, "onboarding-nin")
	token, ok := onboardingToken(c)
	if !ok {
		return
	}
	user, err := h.auth.OnboardingUser(c.Request.Context(), token)
	if err != nil {
		authFailure(c, err)
		return
	}
	// user_id is extracted from the authenticated token
	decision, err := h.kyc.SubmitNINLiveness(c.Request.Context(), user.ID, controllers.IdentityClaim{Number: request.NIN}, providers.NINLivenessInput{ResultToken: request.ResultToken, EncryptedLivenessPayload: request.EncryptedLivenessPayload, SelfieImageBase64: request.SelfieImageBase64, LivenessFrameImagesBase64: request.LivenessFramesBase64, UserID: user.ID.String(), VerificationRequestID: verificationRequestID})
	if err != nil && !(decision != nil && (errors.Is(err, messages.ErrIdentityMismatch) || errors.Is(err, messages.ErrManualReviewPending))) {
		kycFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.NINVerificationDone, decision)
}

func (h *OnboardingHandler) submitBVN(c *gin.Context, request models.VerifyBVNRequest) {
	if !requireDeviceIDHeader(c) {
		return
	}
	verificationRequestID := verificationRequestID(c, "onboarding-bvn")
	token, ok := onboardingToken(c)
	if !ok {
		return
	}
	user, err := h.auth.OnboardingUser(c.Request.Context(), token)
	if err != nil {
		authFailure(c, err)
		return
	}

	// user_id is extracted from the authenticated token
	decision, err := h.kyc.SubmitBVNLiveness(c.Request.Context(), user.ID, controllers.IdentityClaim{Number: request.BVN}, providers.BVNLivenessInput{EncryptedLivenessPayload: request.EncryptedLivenessPayload, SelfieImageBase64: request.SelfieImageBase64, LivenessFrameImagesBase64: request.LivenessFramesBase64, UserID: user.ID.String(), VerificationRequestID: verificationRequestID})
	if err != nil && !(decision != nil && (errors.Is(err, messages.ErrIdentityMismatch) || errors.Is(err, messages.ErrManualReviewPending))) {
		kycFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.BVNVerificationDone, decision)
}

func verificationRequestID(c *gin.Context, prefix string) string {
	id := strings.TrimSpace(c.GetHeader("X-Verification-Request-ID"))
	if id == "" {
		id = strings.TrimSpace(c.GetHeader("X-Verification-Request-Id"))
	}
	if id == "" {
		requestID := strings.TrimSpace(c.GetString("request_id"))
		if requestID == "" {
			requestID = uuid.NewString()
		}
		id = prefix + "-" + requestID
	}
	return id
}

func onboardingDeviceInput(c *gin.Context, request models.PhoneOnboardingRegisterRequest) (controllers.DeviceInput, bool) {
	fingerprint := firstNonEmpty(request.DeviceID, deviceIDHeader(c))
	if fingerprint == "" {
		return controllers.DeviceInput{}, false
	}
	return controllers.DeviceInput{
		Fingerprint: fingerprint,
		Name:        request.DeviceMetadata.DeviceName,
		Latitude:    request.DeviceMetadata.Latitude,
		Longitude:   request.DeviceMetadata.Longitude,
	}, true
}

func deviceIDHeader(c *gin.Context) string {
	return firstNonEmpty(c.GetHeader("X-Device-ID"), c.GetHeader("X-Device-Id"), c.GetHeader("Device-ID"), c.GetHeader("Device-Id"))
}

func requireDeviceIDHeader(c *gin.Context) bool {
	if deviceIDHeader(c) != "" {
		return true
	}
	deviceIDRequired(c)
	return false
}

func deviceIDRequired(c *gin.Context) {
	helpers.Failure(c, http.StatusBadRequest, messages.InvalidFields, gin.H{"code": messages.CodeDeviceFingerprintRequired, "device_id": "X-Device-ID header is required"})
}

func (h *OnboardingHandler) SubmitFacialVerification(c *gin.Context) {
	if !requireDeviceIDHeader(c) {
		return
	}
	var request struct {
		models.PhoneOnboardingTokenRequest
		models.FacialVerificationRequest
	}
	if !bindAndValidate(c, &request) {
		return
	}
	token, ok := onboardingToken(c)
	if !ok {
		return
	}
	user, err := h.auth.OnboardingUser(c.Request.Context(), token)
	if err != nil {
		authFailure(c, err)
		return
	}
	verificationRequestID := verificationRequestID(c, "onboarding-facial")
	result, err := h.kyc.SubmitFacialVerification(c.Request.Context(), user.ID, providers.NINLivenessInput{
		EncryptedLivenessPayload:  request.EncryptedLivenessPayload,
		SelfieImageBase64:         request.SelfieImageBase64,
		LivenessFrameImagesBase64: request.LivenessFramesBase64,
		UserID:                    user.ID.String(),
		VerificationRequestID:     verificationRequestID,
	})
	if err != nil {
		kycFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.FacialVerificationRecorded, result)
}

func (h *OnboardingHandler) SetPassword(c *gin.Context) {
	var request models.PhoneOnboardingPasswordRequest
	if !bindAndValidate(c, &request) {
		return
	}
	if !requireDeviceIDHeader(c) {
		return
	}
	token, ok := onboardingToken(c)
	if !ok {
		return
	}
	user, err := h.auth.OnboardingUser(c.Request.Context(), token)
	if err != nil {
		authFailure(c, err)
		return
	}
	profile, err := h.kyc.Profile(c.Request.Context(), user.ID)
	if err != nil || profile.NINStatus != models.KYCStatusVerified || profile.NINVerifiedAt == nil {
		kycFailure(c, messages.ErrNINRequiredFirst)
		return
	}
	passcode := request.Password
	result, err := h.auth.CompletePhoneOnboarding(c.Request.Context(), token, passcode, clientInfo(c))
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.OnboardingCompleted, gin.H{
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
		"expires_in":    result.ExpiresIn,
		"kyc": gin.H{
			"profile": profile,
			"user":    result.User,
		},
	})
}

func (h *OnboardingHandler) Status(c *gin.Context) {
	if !requireDeviceIDHeader(c) {
		return
	}
	token, ok := onboardingToken(c)
	if !ok {
		return
	}
	user, err := h.auth.OnboardingUser(c.Request.Context(), token)
	if err != nil {
		authFailure(c, err)
		return
	}
	profile, _ := h.kyc.Profile(c.Request.Context(), user.ID)
	helpers.Success(c, http.StatusOK, messages.OnboardingStatusRetrieved, gin.H{
		"kyc":     presentAuthKYCProfile(profile),
		"user":    presentOnboardingUser(user, profile),
		"journey": onboardingJourney(user, profile),
	})
}

// presentOnboardingUser returns only user-owned fields. The KYC profile is a
// sibling object in the response (see presentAuthKYCProfile), never nested
// here; profile is still accepted so name/gender-derived fields can be
// surfaced under the user object.
func presentOnboardingUser(user *models.User, profile *models.KYCProfile) gin.H {
	if user == nil {
		return gin.H{}
	}
	return gin.H{
		"id":                user.ID,
		"email":             user.Email,
		"phone_number":      user.PhoneNumber,
		"nablr_username":    user.NablrUsername,
		"username":          user.NablrUsername,
		"account_number":    user.AccountNumber,
		"full_name":         kycFullName(profile),
		"gender":            valueOrNil(profile, func(p *models.KYCProfile) any { return p.Gender }),
		"password_set":      user.PasswordSet,
		"phone_verified":    user.PhoneVerified,
		"phone_verified_at": user.PhoneVerifiedAt,
		"nin_status":        user.NinStatus,
		"nin_verified":      user.NinVerified,
		"facial_status":     user.FacialStatus,
		"facial_verified":   user.FacialVerified,
		"role":              user.Role,
		"status":            user.Status,
		"email_verified_at": user.EmailVerifiedAt,
		"last_login_at":     user.LastLoginAt,
		"created_at":        user.CreatedAt,
		"updated_at":        user.UpdatedAt,
	}
}

// kycAddress is the nested address object surfaced under the "kyc" key,
// built from the flat address columns stored on the KYC profile.
type kycAddress struct {
	Line1      string `json:"line1,omitempty"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"`
}

// authKYCProfilePresenter embeds the KYC profile so all of its existing
// fields are preserved in the response, while adding a structured "address"
// object.
type authKYCProfilePresenter struct {
	*models.KYCProfile
	Address kycAddress `json:"address"`
}

// presentAuthKYCProfile renders the KYC profile shown as the sibling "kyc"
// object on the onboarding-status and login responses. It is distinct from
// kyc.handler.go's presentKYCProfile (used by the dedicated KYC endpoints),
// which returns a smaller, hand-picked field set.
func presentAuthKYCProfile(profile *models.KYCProfile) any {
	if profile == nil {
		return nil
	}
	return authKYCProfilePresenter{
		KYCProfile: profile,
		Address: kycAddress{
			Line1:      profile.AddressLine1,
			Line2:      profile.AddressLine2,
			City:       profile.City,
			State:      profile.State,
			PostalCode: profile.PostalCode,
			Country:    profile.Country,
		},
	}
}

// kycFullName joins the profile's name parts into a single display string.
func kycFullName(profile *models.KYCProfile) string {
	if profile == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, part := range []string{profile.FirstName, profile.MiddleName, profile.LastName} {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

func onboardingJourney(user *models.User, profile *models.KYCProfile) gin.H {
	phoneVerified := user != nil && user.PhoneVerified
	ninVerified := false
	facialApproved := false
	passwordSet := user != nil && user.PasswordSet
	if profile != nil {
		ninVerified = profile.NINStatus == models.KYCStatusVerified && profile.NINVerifiedAt != nil
	}
	if user != nil {
		facialApproved = user.FacialVerified ||
			strings.EqualFold(user.FacialStatus, "verified") ||
			strings.EqualFold(user.FacialStatus, "approved")
	}
	if ninVerified && !facialApproved {
		facialApproved = true
	}
	completed := phoneVerified && ninVerified && facialApproved && passwordSet
	canSetPassword := phoneVerified && ninVerified && !passwordSet
	nextStep := "phone_verification"
	switch {
	case completed:
		nextStep = "completed"
	case canSetPassword:
		nextStep = "set_password"
	case phoneVerified && !ninVerified:
		nextStep = "nin_facial_verification"
	case phoneVerified && ninVerified && passwordSet:
		nextStep = "login"
	}
	return gin.H{
		"phone_verified":       phoneVerified,
		"nin_verified":         ninVerified,
		"facial_approved":      facialApproved,
		"password_set":         passwordSet,
		"can_set_password":     canSetPassword,
		"completed":            completed,
		"next_step":            nextStep,
		"phone_verified_at":    valueOrNil(user, func(u *models.User) any { return u.PhoneVerifiedAt }),
		"nin_verified_at":      valueOrNil(profile, func(p *models.KYCProfile) any { return p.NINVerifiedAt }),
		"nin_status":           valueOrNil(profile, func(p *models.KYCProfile) any { return p.NINStatus }),
		"facial_status":        valueOrNil(user, func(u *models.User) any { return u.FacialStatus }),
		"onboarding_token_use": "Authorization: Bearer <onboarding_token>",
	}
}

func valueOrNil[T any](value *T, pick func(*T) any) any {
	if value == nil {
		return nil
	}
	return pick(value)
}

func (h *OnboardingHandler) RegisterDevice(c *gin.Context) {
	if h.security == nil {
		helpers.Failure(c, http.StatusServiceUnavailable, messages.InternalError, gin.H{"code": messages.CodeInternalError})
		return
	}
	if !requireDeviceIDHeader(c) {
		return
	}
	token, ok := onboardingToken(c)
	if !ok {
		return
	}
	user, err := h.auth.OnboardingUser(c.Request.Context(), token)
	if err != nil {
		authFailure(c, err)
		return
	}
	var request models.OnboardingDeviceRequest
	if !bindAndValidate(c, &request) {
		return
	}
	fingerprint := firstNonEmpty(request.DeviceID, deviceIDHeader(c))
	if fingerprint == "" {
		helpers.Failure(c, http.StatusBadRequest, messages.InvalidFields, gin.H{"code": messages.CodeDeviceFingerprintRequired, "device_id": "device_id or fingerprint is required"})
		return
	}
	device, created, err := h.security.RegisterDevice(c.Request.Context(), user.ID, controllers.DeviceInput{
		Fingerprint: fingerprint,
		Name:        request.DeviceMetadata.DeviceName,
		Latitude:    request.DeviceMetadata.Latitude,
		Longitude:   request.DeviceMetadata.Longitude,
	})
	if err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.DeviceRegistered, gin.H{"device": device, "created": created})
}

func onboardingToken(c *gin.Context) (string, bool) {
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return "", false
	}
	return parts[1], true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (h *OnboardingHandler) issueOTP(c *gin.Context, userID uuid.UUID, phone string) (*controllers.OTPChallenge, error) {
	return h.otp.Issue(c.Request.Context(), controllers.OTPIssueRequest{UserID: userID,
		Purpose: models.OTPPurposePhoneVerification, Channel: models.OTPChannelSMS, Destination: phone})
}
