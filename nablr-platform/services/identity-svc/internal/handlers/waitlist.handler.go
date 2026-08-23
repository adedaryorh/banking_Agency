package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/controllers"
	models "nabla/identity-svc/internal/models"
)

type WaitlistHandler struct {
	controller controllers.WaitlistController
}

func NewWaitlistHandler(controller controllers.WaitlistController) *WaitlistHandler {
	return &WaitlistHandler{controller: controller}
}

func (h *WaitlistHandler) CheckUsername(c *gin.Context) {
	username := c.Query("username")
	if username == "" {
		helpers.Failure(c, http.StatusBadRequest, messages.InvalidFields, gin.H{"username": "is required"})
		return
	}
	result, err := h.controller.CheckUsername(c.Request.Context(), username)
	if err != nil {
		waitlistFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.WaitlistUsernameChecked, result)
}

func (h *WaitlistHandler) ReserveUsername(c *gin.Context) {
	var request models.WaitlistReserveUsernameRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.ReserveUsername(c.Request.Context(), request, clientInfo(c))
	if err != nil {
		waitlistFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusCreated, messages.WaitlistUsernameReserved, result)
}

func (h *WaitlistHandler) Join(c *gin.Context) {
	var request models.WaitlistJoinRequest
	if !bindAndValidate(c, &request) {
		return
	}
	result, err := h.controller.Join(c.Request.Context(), request)
	if err != nil {
		waitlistFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusOK, messages.WaitlistJoined, result)
}

func waitlistFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, messages.ErrUsernameInvalid):
		helpers.Failure(c, http.StatusUnprocessableEntity, err.Error(), gin.H{"code": messages.CodeUsernameInvalid})
	case errors.Is(err, messages.ErrUsernameReserved):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodeUsernameReserved})
	case errors.Is(err, messages.ErrUsernameTaken):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodeUsernameTaken})
	case errors.Is(err, messages.ErrWaitlistHoldExpired):
		helpers.Failure(c, http.StatusGone, err.Error(), gin.H{"code": messages.CodeWaitlistHoldExpired})
	case errors.Is(err, messages.ErrWaitlistEmailExists):
		helpers.Failure(c, http.StatusConflict, err.Error(), gin.H{"code": messages.CodeWaitlistEmailExists})
	default:
		authFailure(c, err)
	}
}
