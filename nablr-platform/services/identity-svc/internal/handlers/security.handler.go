package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/controllers"
	"nabla/identity-svc/internal/middleware"
	models "nabla/identity-svc/internal/models"
)

type SecurityHandler struct {
	security controllers.SecurityController
	otp      controllers.OTPController
	kyc      controllers.KYCController
}

func NewSecurityHandler(
	security controllers.SecurityController,
	otp controllers.OTPController,
	kyc controllers.KYCController,
) *SecurityHandler {
	return &SecurityHandler{security: security, otp: otp, kyc: kyc}
}

func (h *SecurityHandler) SetPIN(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	var request models.SetPINRequest
	if !bindAndValidate(c, &request) {
		return
	}

	if err := h.security.SetPIN(c.Request.Context(), userID, request.PIN); err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusCreated, messages.PINSet, nil)
}

func (h *SecurityHandler) ChangePIN(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	var request models.ChangePINRequest
	if !bindAndValidate(c, &request) {
		return
	}

	if err := h.security.ChangePIN(c.Request.Context(), userID, request.CurrentPIN, request.NewPIN); err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.PINChanged, nil)
}

func (h *SecurityHandler) ResetPIN(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	var request models.ResetPINRequest
	if !bindAndValidate(c, &request) {
		return
	}

	if err := h.security.ResetPIN(c.Request.Context(), userID, request.ChallengeToken, request.Code, request.NewPIN); err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.PINReset, nil)
}

func (h *SecurityHandler) PINStatus(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	hasPIN, err := h.security.HasPIN(c.Request.Context(), userID)
	if err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.PINStatusRetrieved, gin.H{"has_pin": hasPIN})
}

func (h *SecurityHandler) RequestOTP(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	var request models.RequestOTPRequest
	if !bindAndValidate(c, &request) {
		return
	}

	challenge, err := h.otp.Issue(c.Request.Context(), controllers.OTPIssueRequest{
		UserID:  userID,
		Purpose: models.OTPPurpose(request.Purpose),
		Channel: models.OTPChannel(request.Channel),
	})
	if err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.VerificationCodeSent, challenge)
}

func (h *SecurityHandler) VerifyPhone(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	var request models.VerifyPhoneRequest
	if !bindAndValidate(c, &request) {
		return
	}

	challenge, err := h.otp.Issue(c.Request.Context(), controllers.OTPIssueRequest{
		UserID:      userID,
		Purpose:     models.OTPPurposePhoneVerification,
		Channel:     models.OTPChannelSMS,
		Destination: request.PhoneNumber,
	})
	if err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.VerificationCodeSent, challenge)
}

func (h *SecurityHandler) ConfirmPhone(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	var request models.ConfirmPhoneRequest
	if !bindAndValidate(c, &request) {
		return
	}

	ctx := c.Request.Context()
	record, err := h.otp.Verify(ctx, nil, request.ChallengeToken, request.Code, models.OTPPurposePhoneVerification)
	if err != nil {
		securityFailure(c, err)
		return
	}
	profile, err := h.kyc.MarkPhoneVerified(ctx, userID, record.Destination)
	if err != nil {
		kycFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.PhoneVerified, presentKYCProfile(profile))
}

func (h *SecurityHandler) ListDevices(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	devices, err := h.security.ListDevices(c.Request.Context(), userID)
	if err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.DevicesRetrieved, devices)
}

func (h *SecurityHandler) TrustDevice(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	var request struct {
		DeviceID       string `json:"device_id" validate:"required,uuid4"`
		ChallengeToken string `json:"challenge_token" validate:"required"`
		Code           string `json:"code" validate:"required,min=4,max=8"`
	}
	if !bindAndValidate(c, &request) {
		return
	}

	deviceID, err := uuid.Parse(request.DeviceID)
	if err != nil {
		helpers.Failure(c, http.StatusBadRequest, messages.InvalidDeviceID, gin.H{"code": messages.CodeInvalidDeviceID})
		return
	}

	device, err := h.security.TrustDevice(c.Request.Context(), userID, deviceID, request.ChallengeToken, request.Code)
	if err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.DeviceTrusted, device)
}

func (h *SecurityHandler) BlockDevice(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	deviceID, err := uuid.Parse(c.Param("deviceId"))
	if err != nil {
		helpers.Failure(c, http.StatusBadRequest, messages.InvalidDeviceID, gin.H{"code": messages.CodeInvalidDeviceID})
		return
	}

	if err := h.security.BlockDevice(c.Request.Context(), userID, deviceID); err != nil {
		securityFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.DeviceBlocked, nil)
}

func securityFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, messages.ErrPINNotSet):
		helpers.Failure(c, http.StatusPreconditionFailed, err.Error(), gin.H{"code": messages.CodePINNotSet})
	case errors.Is(err, messages.ErrPINAlreadySet):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodePINAlreadySet})
	case errors.Is(err, messages.ErrPINInvalid):
		helpers.Failure(c, http.StatusUnauthorized, err.Error(), gin.H{"code": messages.CodePINInvalid})
	case errors.Is(err, messages.ErrPINLocked):
		helpers.Failure(c, http.StatusLocked, err.Error(), gin.H{"code": messages.CodePINLocked})
	case errors.Is(err, messages.ErrPINWeak):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": messages.CodePINWeak})
	case errors.Is(err, messages.ErrPINSameAsCurrent):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": messages.CodePINUnchanged})
	case errors.Is(err, messages.ErrOTPNotFound):
		helpers.Failure(c, http.StatusNotFound, err.Error(), gin.H{"code": messages.CodeOTPNotFound})
	case errors.Is(err, messages.ErrOTPExpired):
		helpers.Failure(c, http.StatusGone, err.Error(), gin.H{"code": messages.CodeOTPExpired})
	case errors.Is(err, messages.ErrOTPConsumed):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodeOTPConsumed})
	case errors.Is(err, messages.ErrOTPInvalid):
		helpers.Failure(c, http.StatusUnauthorized, err.Error(), gin.H{"code": messages.CodeOTPInvalid})
	case errors.Is(err, messages.ErrOTPAttempts):
		helpers.Failure(c, http.StatusTooManyRequests, err.Error(), gin.H{"code": messages.CodeOTPAttemptsExceeded})
	case errors.Is(err, messages.ErrOTPThrottled):
		helpers.Failure(c, http.StatusTooManyRequests, err.Error(), gin.H{"code": messages.CodeOTPThrottled})
	case errors.Is(err, messages.ErrOTPPurposeMismatch):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": messages.CodeOTPPurposeMismatch})
	case errors.Is(err, messages.ErrNoOTPDestination):
		helpers.Failure(c, http.StatusPreconditionFailed, err.Error(), gin.H{"code": messages.CodeNoOTPDestination})
	case errors.Is(err, messages.ErrDeviceNotFound):
		helpers.Failure(c, http.StatusNotFound, err.Error(), gin.H{"code": messages.CodeDeviceNotFound})
	case errors.Is(err, messages.ErrDeviceBlocked):
		helpers.Failure(c, http.StatusForbidden, err.Error(), gin.H{"code": messages.CodeDeviceBlocked})
	default:
		helpers.AppErrorFailure(c, *helpers.NewAppInternalError(err))
	}
}
