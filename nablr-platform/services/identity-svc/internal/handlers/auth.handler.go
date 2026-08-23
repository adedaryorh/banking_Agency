package handlers

import (
	"errors"
	"log"
	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/controllers"
	"nabla/identity-svc/internal/middleware"
	"nabla/identity-svc/internal/providers"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	models "nabla/identity-svc/internal/models"

	repo "nabla/identity-svc/internal/repository"
)

type AuthHandler struct {
	controller controllers.AuthController
	kyc        controllers.KYCController
}

func NewAuthHandler(controller controllers.AuthController, kyc controllers.KYCController) *AuthHandler {
	return &AuthHandler{controller: controller, kyc: kyc}
}

func (h *AuthHandler) Register(c *gin.Context) {
	var request models.RegisterRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.Register(c.Request.Context(), request, c.GetHeader("X-Waitlist-Reservation-Token"))
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusCreated, messages.RegistrationSuccessful, result)
}

func (h *AuthHandler) Login(c *gin.Context) {
	var request models.LoginRequest
	if !bindAndValidate(c, &request) {
		return
	}
	if request.Email == "" && request.PhoneNumber == "" {
		helpers.Failure(c, http.StatusBadRequest, messages.InvalidFields, gin.H{
			"email":        "either email or phone_number is required",
			"phone_number": "either email or phone_number is required",
		})
		return
	}
	result, err := h.controller.Login(c.Request.Context(), request, clientInfo(c))
	if err != nil {
		var deactivated *controllers.AccountDeactivatedError
		if errors.As(err, &deactivated) {
			helpers.Failure(c, http.StatusForbidden, messages.ErrAccountDeactivated.Error(), gin.H{
				"code":          messages.CodeAccountDeactivated,
				"restore_token": deactivated.RestoreToken,
			})
			return
		}
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.LoginSuccessful, gin.H{
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
		"expires_in":    result.ExpiresIn,
		"kyc":           presentAuthKYCProfile(result.KYC),
		"user":          presentOnboardingUser(result.User, result.KYC),
	})
}

func (h *AuthHandler) Google(c *gin.Context) {
	var request models.GoogleAuthRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.Google(c.Request.Context(), request, clientInfo(c))
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.AuthenticationSuccess, result)
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	var request models.RefreshTokenRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.Refresh(c.Request.Context(), request, clientInfo(c))
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.TokenRefreshSuccessful, result)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	var request models.LogoutRequest
	if !bindAndValidate(c, &request) {
		return
	}
	if err := h.controller.Logout(c.Request.Context(), request); err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.LogoutSuccessful, nil)
}

func (h *AuthHandler) VerifyEmail(c *gin.Context) {
	var request models.VerifyEmailRequest
	if !bindAndValidate(c, &request) {
		return
	}
	if err := h.controller.VerifyEmail(c.Request.Context(), request); err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.EmailVerified, nil)
}

func (h *AuthHandler) ResendVerification(c *gin.Context) {
	var request models.ResendVerificationRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.ResendVerification(c.Request.Context(), request)
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.VerificationSent, result)
}

func (h *AuthHandler) ForgotPassword(c *gin.Context) {
	var request models.ForgotPasswordRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.ForgotPassword(c.Request.Context(), request)
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.PasswordResetSent, result)
}

func (h *AuthHandler) ResetPassword(c *gin.Context) {
	var request models.ResetPasswordRequest
	if !bindAndValidate(c, &request) {
		return
	}
	if err := h.controller.ResetPassword(c.Request.Context(), request); err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.PasswordResetSuccess, nil)
}

func (h *AuthHandler) RequestPhonePasswordReset(c *gin.Context) {
	var request models.PhonePasswordResetRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.RequestPhonePasswordReset(c.Request.Context(), request)
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.PasswordResetCodeSent, result)
}

func (h *AuthHandler) ConfirmPhonePasswordReset(c *gin.Context) {
	var request models.ConfirmPhonePasswordResetRequest
	if !bindAndValidate(c, &request) {
		return
	}
	if err := h.controller.ConfirmPhonePasswordReset(c.Request.Context(), request); err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.PasswordResetSuccess, nil)
}

func (h *AuthHandler) RequestMagicLink(c *gin.Context) {
	var request models.MagicLinkRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.RequestMagicLink(c.Request.Context(), request, clientInfo(c))
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.MagicLinkSent, result)
}

func (h *AuthHandler) ConfirmMagicLink(c *gin.Context) {
	var request models.ConfirmMagicLinkRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.ConfirmMagicLink(c.Request.Context(), request, clientInfo(c))
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.AuthenticationSuccess, result)
}

// RequestClosure is step 1 of account closure: "Tell us why". It only
// records the reason — nothing about the account changes yet.
func (h *AuthHandler) RequestClosure(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}
	var request models.RequestAccountClosureRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.RequestAccountClosure(c.Request.Context(), userID, request.Reason, request.ReasonNote)
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusCreated, messages.AccountClosureRequested, result)
}

// VerifyClosureFace is step 2: re-run the facial liveness check, then mint a
// confirmation code the app displays directly (no SMS/email leg).
func (h *AuthHandler) VerifyClosureFace(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}
	var request struct {
		RequestID string `json:"request_id" validate:"required,uuid4"`
		models.FacialVerificationRequest
	}
	if !bindAndValidate(c, &request) {
		return
	}
	requestID, err := uuid.Parse(request.RequestID)
	if err != nil {
		helpers.Failure(c, http.StatusUnprocessableEntity, messages.InvalidFields, gin.H{"request_id": "must be a valid UUID"})
		return
	}
	// TODO: uncomment this later
	_ = providers.NINLivenessInput{}
	// verificationRequestID := verificationRequestID(c, "closure-facial")
	// if _, err := h.kyc.SubmitFacialVerification(c.Request.Context(), userID, providers.NINLivenessInput{
	// 	EncryptedLivenessPayload:  request.EncryptedLivenessPayload,
	// 	SelfieImageBase64:         request.SelfieImageBase64,
	// 	LivenessFrameImagesBase64: request.LivenessFramesBase64,
	// 	UserID:                    userID.String(),
	// 	VerificationRequestID:     verificationRequestID,
	// }); err != nil {
	// 	kycFailure(c, err)
	// 	return
	// }
	challenge, err := h.controller.IssueAccountClosureCode(c.Request.Context(), userID, requestID)
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.AccountClosureCodeIssued, challenge)
}

// ConfirmClosure is step 3: the code the app just displayed, typed back in.
// On success the account is deactivated (wallet-balance-zero is checked here
// too, server-side).
func (h *AuthHandler) ConfirmClosure(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}
	var request models.ConfirmAccountClosureRequest
	if !bindAndValidate(c, &request) {
		return
	}
	requestID, err := uuid.Parse(request.RequestID)
	if err != nil {
		helpers.Failure(c, http.StatusUnprocessableEntity, messages.InvalidFields, gin.H{"request_id": "must be a valid UUID"})
		return
	}
	result, err := h.controller.ConfirmAccountClosure(c.Request.Context(), userID, requestID, request.Code)
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.AccountDeactivated, result)
}

// ReactivateAccount is the self-serve in-app restore path: still logged in
// (refresh tokens survive deactivation's revocation only until they expire,
// so this mainly covers a fresh login-then-immediately-undo).
func (h *AuthHandler) ReactivateAccount(c *gin.Context) {
	userID, ok := getUserID(c)
	if !ok {
		return
	}
	result, err := h.controller.ReactivateAccount(c.Request.Context(), userID)
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.AccountRestored, result)
}

// RestoreAccount is the other restore path: redeems the token Login issued
// when it rejected a deactivated account, without requiring an access token.
func (h *AuthHandler) RestoreAccount(c *gin.Context) {
	var request models.RestoreAccountRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.RestoreWithToken(c.Request.Context(), request.Token)
	if err != nil {
		authFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.AccountRestored, result)
}

func bindAndValidate(c *gin.Context, destination any) bool {
	if err := helpers.DecodeJSON(c.Request.Body, destination, helpers.JSONDecodeOptions{
		DisallowUnknownFields: true,
		UseNumber:             true,
	}); err != nil {
		helpers.Failure(c, http.StatusBadRequest, messages.InvalidRequestBody, gin.H{"code": messages.CodeInvalidJSON})
		return false
	}
	if err := helpers.Struct(destination); err != nil {
		helpers.Failure(c, http.StatusUnprocessableEntity, messages.InvalidFields, err)
		return false
	}
	return true
}

func authFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, messages.ErrEmailExists):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodeEmailAlreadyExists})
	case errors.Is(err, messages.ErrPhoneExists):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodePhoneAlreadyExists})
	case errors.Is(err, messages.ErrInvalidPhoneNumber):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": messages.CodeInvalidPhoneNumber})
	case errors.Is(err, repo.ErrConflict):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodeConflict})
	case errors.Is(err, messages.ErrInvalidCredentials):
		helpers.Failure(c, http.StatusUnauthorized, err.Error(), gin.H{"code": messages.CodeInvalidCredentials})
	case errors.Is(err, messages.ErrInvalidToken):
		helpers.Failure(c, http.StatusUnauthorized, err.Error(), gin.H{"code": messages.CodeInvalidToken})
	case errors.Is(err, messages.ErrWaitlistHoldExpired):
		helpers.Failure(c, http.StatusGone, err.Error(), gin.H{"code": messages.CodeInvalidToken})
	case errors.Is(err, messages.ErrUsernameTaken):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodeUsernameTaken})
	case errors.Is(err, messages.ErrEmailNotVerified):
		helpers.Failure(c, http.StatusForbidden, err.Error(), gin.H{"code": messages.CodeEmailNotVerified})
	case errors.Is(err, messages.ErrAccountSuspended):
		helpers.Failure(c, http.StatusForbidden, err.Error(), gin.H{"code": messages.CodeAccountUnavailable})
	case errors.Is(err, messages.ErrProviderUnavailable):
		helpers.Failure(c, http.StatusServiceUnavailable, err.Error(), gin.H{"code": messages.CodeProviderUnavailable})
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
	case errors.Is(err, messages.ErrAccountNotDeactivated):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": messages.CodeAccountNotDeactivated})
	case errors.Is(err, messages.ErrAccountAlreadyDeactivated):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": messages.CodeAccountAlreadyDeactivated})

	case errors.Is(err, messages.ErrAccountDeactivated):
		helpers.Failure(c, http.StatusForbidden, err.Error(), gin.H{"code": messages.CodeAccountDeactivated})
	case errors.Is(err, messages.ErrClosureReasonInvalid):
		helpers.Failure(c, http.StatusUnprocessableEntity, err.Error(), gin.H{"code": messages.CodeClosureReasonInvalid})
	case errors.Is(err, messages.ErrClosureRequestNotFound):
		helpers.Failure(c, http.StatusNotFound, err.Error(), gin.H{"code": messages.CodeClosureRequestNotFound})
	case errors.Is(err, messages.ErrClosureFaceNotVerified):
		helpers.Failure(c, http.StatusForbidden, err.Error(), gin.H{"code": messages.CodeClosureFaceNotVerified})
	case errors.Is(err, messages.ErrClosureCodeExpired):
		helpers.Failure(c, http.StatusGone, err.Error(), gin.H{"code": messages.CodeClosureCodeExpired})
	case errors.Is(err, messages.ErrClosureCodeInvalid):
		helpers.Failure(c, http.StatusUnauthorized, err.Error(), gin.H{"code": messages.CodeClosureCodeInvalid})
	case errors.Is(err, messages.ErrClosureCodeAttempts):
		helpers.Failure(c, http.StatusTooManyRequests, err.Error(), gin.H{"code": messages.CodeClosureCodeAttempts})
	case errors.Is(err, messages.ErrClosureBalanceNotZero):
		helpers.Failure(c, http.StatusUnprocessableEntity, err.Error(), gin.H{"code": messages.CodeClosureBalanceNotZero})
	case errors.Is(err, messages.ErrPINWeak):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": messages.CodePINWeak})
	case errors.Is(err, messages.ErrWeakPassword):
		helpers.Failure(c, http.StatusUnprocessableEntity, err.Error(), gin.H{"code": messages.CodePasswordWeak})
	default:
		log.Printf("auth request failed path=%s error=%v", c.Request.URL.Path, err)
		helpers.AppErrorFailure(c, *helpers.NewAppInternalError(err))
	}
}

func clientInfo(c *gin.Context) controllers.ClientInfo {
	ip := strings.TrimSpace(c.ClientIP())
	if parsed := net.ParseIP(ip); parsed == nil {
		ip = ""
	}
	return controllers.ClientInfo{
		UserAgent: truncate(c.Request.UserAgent(), 1000),
		IPAddress: truncate(ip, 64),
	}
}

func truncate(value string, length int) string {
	if len(value) <= length {
		return value
	}
	return value[:length]
}

func getUserID(c *gin.Context) (uuid.UUID, bool) {
	return middleware.CurrentUserID(c)
}
