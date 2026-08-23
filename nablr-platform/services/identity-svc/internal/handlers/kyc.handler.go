package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/controllers"
	"nabla/identity-svc/internal/middleware"
	models "nabla/identity-svc/internal/models"
	"nabla/identity-svc/internal/providers"
)

type KYCHandler struct {
	controller controllers.KYCController
	limits     controllers.LimitController
}

func NewKYCHandler(controller controllers.KYCController, limits controllers.LimitController) *KYCHandler {
	return &KYCHandler{controller: controller, limits: limits}
}

func (h *KYCHandler) Profile(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	profile, err := h.controller.Profile(c.Request.Context(), userID)
	if err != nil {
		kycFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.KYCProfileRetrieved, presentKYCProfile(profile))
}

func (h *KYCHandler) SubmitBVN(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}
	var request struct {
		BVN string `json:"bvn" binding:"required,len=11,numeric"`
	}
	if !bindAndValidate(c, &request) {
		return
	}

	// Simple BVN lookup (no liveness verification)
	profile, err := h.controller.SubmitBVN(c.Request.Context(), userID, controllers.IdentityClaim{
		Number: request.BVN,
	})
	if err != nil {
		kycFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.BVNVerified, presentKYCProfile(profile))
}

func (h *KYCHandler) SubmitNIN(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	var request struct {
		NIN string `json:"nin" binding:"required,len=11,numeric"`
	}
	if !bindAndValidate(c, &request) {
		return
	}

	// Simple NIN lookup (no liveness verification)
	profile, err := h.controller.SubmitNIN(c.Request.Context(), userID, controllers.IdentityClaim{
		Number: request.NIN,
	})
	if err != nil {
		kycFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.NINVerificationDone, presentKYCProfile(profile))
}

func (h *KYCHandler) SubmitAddress(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	var request models.SubmitAddressRequest
	if !bindAndValidate(c, &request) {
		return
	}

	profile, err := h.controller.SubmitAddress(c.Request.Context(), userID, controllers.AddressInput{
		Line1:      request.AddressLine1,
		Line2:      request.AddressLine2,
		City:       request.City,
		State:      request.State,
		PostalCode: request.PostalCode,
		Country:    "NG",
	})
	if err != nil {
		kycFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.AddressSubmitted, presentKYCProfile(profile))
}

func (h *KYCHandler) Attempts(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	attempts, err := h.controller.Attempts(c.Request.Context(), userID, limit)
	if err != nil {
		kycFailure(c, err)
		return
	}

	view := make([]gin.H, 0, len(attempts))
	for _, attempt := range attempts {
		view = append(view, gin.H{
			"id":             attempt.ID,
			"kind":           attempt.Kind,
			"status":         attempt.Status,
			"match_score":    attempt.MatchScore,
			"failure_reason": attempt.FailureReason,
			"created_at":     attempt.CreatedAt,
		})
	}
	helpers.Success(c, http.StatusOK, messages.AttemptsRetrieved, view)
}

func (h *KYCHandler) Limits(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
		return
	}

	currency := strings.ToUpper(c.DefaultQuery("currency", "NGN"))
	snapshot, err := h.limits.Snapshot(c.Request.Context(), userID, currency)
	if err != nil {
		kycFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.LimitsRetrieved, snapshot)
}

func (h *KYCHandler) Details(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		helpers.Failure(c, http.StatusUnauthorized, messages.AuthenticationRequired, gin.H{"code": messages.CodeAuthenticationRequired})
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
	result, err := h.controller.LookupDetails(c.Request.Context(), userID, request.PhoneNumber, request.BVN, request.NIN)
	if err != nil {
		kycFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.KYCDetailsRetrieved, result)
}

func presentKYCProfile(profile *models.KYCProfile) gin.H {
	if profile == nil {
		return gin.H{}
	}
	return gin.H{
		"tier":              profile.Tier,
		"phone_number":      profile.PhoneNumber,
		"phone_status":      profile.PhoneStatus,
		"bvn_status":        profile.BVNStatus,
		"bvn_last4":         profile.BVNLast4,
		"nin_status":        profile.NINStatus,
		"nin_last4":         profile.NINLast4,
		"address_status":    profile.AddressStatus,
		"first_name":        profile.FirstName,
		"middle_name":       profile.MiddleName,
		"last_name":         profile.LastName,
		"date_of_birth":     profile.DateOfBirth,
		"gender":            profile.Gender,
		"address_line1":     profile.AddressLine1,
		"address_line2":     profile.AddressLine2,
		"city":              profile.City,
		"state":             profile.State,
		"postal_code":       profile.PostalCode,
		"country":           profile.Country,
		"reject_reason":     profile.RejectReason,
		"bvn_verified_at":   profile.BVNVerifiedAt,
		"nin_verified_at":   profile.NINVerifiedAt,
		"phone_verified_at": profile.PhoneVerifiedAt,
	}
}

func kycFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, messages.ErrKYCProfileNotFound):
		helpers.Failure(c, http.StatusNotFound, err.Error(), gin.H{"code": messages.CodeKYCProfileNotFound})
	case errors.Is(err, messages.ErrInvalidBVN):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": messages.CodeInvalidBVN})
	case errors.Is(err, messages.ErrInvalidNIN):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": messages.CodeInvalidNIN})
	case errors.Is(err, messages.ErrIdentityNumberRequired):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": "SINGLE_IDENTITY_NUMBER_REQUIRED"})
	case errors.Is(err, messages.ErrIdentityInUse):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodeIdentityInUse})
	case errors.Is(err, messages.ErrAlreadyVerified):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodeAlreadyVerified})
	case errors.Is(err, messages.ErrVerificationThrottled):
		helpers.Failure(c, http.StatusTooManyRequests, err.Error(), gin.H{"code": messages.CodeVerificationThrottled})
	case errors.Is(err, messages.ErrIdentityMismatch):
		helpers.Failure(c, http.StatusUnprocessableEntity, err.Error(), gin.H{"code": messages.CodeIdentityMismatch})
	case errors.Is(err, messages.ErrManualReviewPending):
		helpers.Success(c, http.StatusAccepted, err.Error(), gin.H{"code": messages.CodeManualReviewPending})
	case errors.Is(err, messages.ErrPhoneNotVerified):
		helpers.Failure(c, http.StatusPreconditionFailed, err.Error(), gin.H{"code": messages.CodePhoneNotVerified})
	case errors.Is(err, messages.ErrPhoneLookupForbidden):
		helpers.Failure(c, http.StatusForbidden, err.Error(), gin.H{"code": messages.CodePhoneLookupForbidden})
	case errors.Is(err, messages.ErrNINLookupForbidden):
		helpers.Failure(c, http.StatusForbidden, err.Error(), gin.H{"code": messages.CodeNINLookupForbidden})
	case errors.Is(err, messages.ErrBVNRequiredFirst):
		helpers.Failure(c, http.StatusPreconditionFailed, err.Error(), gin.H{"code": messages.CodeBVNRequiredFirst})
	case errors.Is(err, messages.ErrNINRequiredFirst):
		helpers.Failure(c, http.StatusPreconditionFailed, err.Error(), gin.H{"code": messages.CodeNINRequiredFirst})
	case errors.Is(err, messages.ErrAddressIncomplete):
		helpers.Failure(c, http.StatusBadRequest, err.Error(), gin.H{"code": messages.CodeAddressIncomplete})
	case errors.Is(err, messages.ErrInvalidLivenessPayload):
		helpers.Failure(c, http.StatusUnprocessableEntity, err.Error(), gin.H{"code": messages.CodeInvalidLivenessPayload})
	case errors.Is(err, messages.ErrLivenessRejected):
		helpers.Failure(c, http.StatusUnprocessableEntity, err.Error(), gin.H{"code": messages.CodeLivenessRejected})
	case errors.Is(err, messages.ErrLimitsNotConfigured):
		helpers.Failure(c, http.StatusServiceUnavailable, err.Error(), gin.H{"code": messages.CodeLimitsNotConfigured})
	case errors.Is(err, messages.ErrAccountRestricted):
		helpers.Failure(c, http.StatusForbidden, err.Error(), gin.H{"code": messages.CodeAccountRestricted})
	case errors.Is(err, providers.ErrNotConfigured):
		helpers.Failure(c, http.StatusServiceUnavailable, "identity lookup provider is not configured", gin.H{"code": messages.CodeProviderUnavailable})
	case errors.Is(err, providers.ErrUnavailable):
		helpers.Failure(c, http.StatusBadGateway, "identity lookup provider is unavailable", gin.H{"code": messages.CodeProviderUnavailable})
	case errors.Is(err, providers.ErrRejected):
		helpers.Failure(c, http.StatusBadGateway, "identity lookup provider rejected the request", gin.H{"code": messages.CodeProviderUnavailable})
	default:
		helpers.AppErrorFailure(c, *helpers.NewAppInternalError(err))
	}
}

// ConvertBase64ToImage decodes a base64 string and returns it as an image
func (h *KYCHandler) ConvertBase64ToImage(c *gin.Context) {
	var request struct {
		Base64Data string `json:"base64_data" binding:"required"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		helpers.Failure(c, http.StatusBadRequest, "Invalid request: base64_data is required", gin.H{"code": "INVALID_REQUEST"})
		return
	}

	// Decode base64 string
	imageData, err := helpers.DecodeBase64Image(request.Base64Data)
	if err != nil {
		helpers.Failure(c, http.StatusBadRequest, "Invalid base64 data", gin.H{"code": "INVALID_BASE64"})
		return
	}

	// Detect content type
	contentType := http.DetectContentType(imageData)

	// Set appropriate headers
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", "inline; filename=\"photo.jpg\"")
	c.Header("Cache-Control", "no-cache, no-store, must-revalidate")

	// Return the image
	c.Data(http.StatusOK, contentType, imageData)
}
