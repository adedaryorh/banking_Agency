package handlers

import (
	"errors"
	"io"
	"net/http"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/controllers"
	"nabla/identity-svc/internal/middleware"

	"github.com/gin-gonic/gin"
)

// AvatarHandler serves a user their own profile-image upload/download pair.
// Upload accepts a multipart image/* field named "image" and records the
// stored object key on the user; Download streams the current image back.
type AvatarHandler struct {
	controller *controllers.AvatarController
}

func NewAvatarHandler(c *controllers.AvatarController) *AvatarHandler {
	return &AvatarHandler{controller: c}
}

func (h *AvatarHandler) Upload(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, messages.MaxUploadImageBytes+1024*1024)
	if err := c.Request.ParseMultipartForm(messages.MaxUploadImageBytes); err != nil {
		helpers.Failure(c, http.StatusRequestEntityTooLarge, messages.ImageTooLarge, gin.H{"code": messages.CodeImageTooLarge})
		return
	}
	file, header, err := c.Request.FormFile("image")
	if err != nil {
		helpers.Failure(c, http.StatusUnprocessableEntity, messages.ImageRequired, gin.H{"code": messages.CodeImageRequired})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, messages.MaxUploadImageBytes+1))
	if err != nil || int64(len(data)) > messages.MaxUploadImageBytes {
		helpers.Failure(c, http.StatusRequestEntityTooLarge, messages.ImageTooLarge, gin.H{"code": messages.CodeImageTooLarge})
		return
	}
	contentType := http.DetectContentType(data)
	if contentType == "application/octet-stream" {
		contentType = header.Header.Get("Content-Type")
	}
	user, err := h.controller.Upload(c.Request.Context(), userID, controllers.AvatarUploadInput{
		Filename:    header.Filename,
		ContentType: contentType,
		Data:        data,
	})
	if err != nil {
		avatarFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusCreated, messages.ImageUploaded, gin.H{
		"avatar_url":   user.AvatarURL,
		"content_type": contentType,
		"size_bytes":   len(data),
	})
}

func (h *AvatarHandler) Download(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	result, err := h.controller.Download(c.Request.Context(), userID)
	if err != nil {
		avatarFailure(c, err)
		return
	}
	c.Header("Content-Disposition", `inline; filename="`+result.Filename+`"`)
	c.Data(http.StatusOK, result.ContentType, result.Data)
}

func avatarFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, messages.ErrInvalidImage):
		helpers.Failure(c, http.StatusUnprocessableEntity, err.Error(), gin.H{"code": messages.CodeInvalidImage})
	case errors.Is(err, messages.ErrAvatarNotFound):
		helpers.Failure(c, http.StatusNotFound, err.Error(), gin.H{"code": messages.CodeAvatarNotFound})
	case errors.Is(err, messages.ErrProviderUnavailable):
		helpers.Failure(c, http.StatusServiceUnavailable, messages.StorageUnavailable, gin.H{"code": messages.CodeStorageUnavailable})
	default:
		kycFailure(c, err)
	}
}