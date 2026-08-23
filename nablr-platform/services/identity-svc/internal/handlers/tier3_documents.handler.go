package handlers

import (
	"errors"
	"io"
	"net/http"
	"time"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/controllers"
	"nabla/identity-svc/internal/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Tier3DocumentHandler struct {
	controller *controllers.Tier3DocumentController
}

func NewTier3DocumentHandler(c *controllers.Tier3DocumentController) *Tier3DocumentHandler {
	return &Tier3DocumentHandler{controller: c}
}

func (h *Tier3DocumentHandler) Upload(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, messages.MaxTier3DocumentBytes+1024*1024)
	if err := c.Request.ParseMultipartForm(messages.MaxTier3DocumentBytes); err != nil {
		helpers.Failure(c, http.StatusRequestEntityTooLarge, messages.DocumentTooLarge, gin.H{"code": messages.CodeDocumentTooLarge})
		return
	}
	documentType, issueDateRaw := c.PostForm("document_type"), c.PostForm("issue_date")
	issueDate, err := time.Parse("2006-01-02", issueDateRaw)
	if err != nil {
		helpers.Failure(c, http.StatusUnprocessableEntity, messages.InvalidIssueDate, gin.H{"code": messages.CodeInvalidIssueDate})
		return
	}
	file, header, err := c.Request.FormFile("document")
	if err != nil {
		helpers.Failure(c, http.StatusUnprocessableEntity, messages.DocumentRequired, gin.H{"code": messages.CodeDocumentRequired})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, messages.MaxTier3DocumentBytes+1))
	if err != nil || int64(len(data)) > messages.MaxTier3DocumentBytes {
		helpers.Failure(c, http.StatusRequestEntityTooLarge, messages.DocumentTooLarge, gin.H{"code": messages.CodeDocumentTooLarge})
		return
	}
	contentType := http.DetectContentType(data)
	if contentType == "application/octet-stream" {
		contentType = header.Header.Get("Content-Type")
	}
	doc, err := h.controller.Upload(c.Request.Context(), userID, controllers.UploadDocumentInput{DocumentType: documentType, Filename: header.Filename, ContentType: contentType, IssueDate: issueDate, Data: data})
	if err != nil {
		tier3DocumentFailure(c, err)
		return
	}
	helpers.Success(c, http.StatusCreated, messages.DocumentUploaded, gin.H{"id": doc.ID, "document_type": doc.DocumentType, "filename": doc.OriginalFilename, "content_type": doc.ContentType, "size_bytes": doc.SizeBytes, "status": doc.Status, "uploaded_at": doc.UploadedAt})
}

func (h *Tier3DocumentHandler) Download(c *gin.Context) {
	userID, ok := middleware.CurrentUserID(c)
	if !ok {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	documentID, err := uuid.Parse(c.Param("document_id"))
	if err != nil {
		helpers.Failure(c, http.StatusBadRequest, messages.InvalidDocumentID, gin.H{"code": messages.CodeInvalidDocumentID})
		return
	}
	result, err := h.controller.Download(c.Request.Context(), userID, documentID)
	if err != nil {
		tier3DocumentFailure(c, err)
		return
	}
	c.Header("Content-Disposition", `inline; filename="`+result.Filename+`"`)
	c.Data(http.StatusOK, result.ContentType, result.Data)
}
func tier3DocumentFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, messages.ErrInvalidDocument):
		helpers.Failure(c, http.StatusUnprocessableEntity, err.Error(), gin.H{"code": messages.CodeInvalidDocument})
	case errors.Is(err, messages.ErrDocumentNotFound):
		helpers.Failure(c, http.StatusNotFound, err.Error(), gin.H{"code": messages.CodeDocumentNotFound})
	case errors.Is(err, messages.ErrProviderUnavailable):
		helpers.Failure(c, http.StatusServiceUnavailable, messages.StorageUnavailable, gin.H{"code": messages.CodeStorageUnavailable})
	default:
		kycFailure(c, err)
	}
}
