package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/models"
	"nabla/identity-svc/internal/providers"
	repo "nabla/identity-svc/internal/repository"

	"github.com/google/uuid"
)

type UploadDocumentInput struct {
	DocumentType, Filename, ContentType string
	IssueDate                           time.Time
	Data                                []byte
}
type Tier3DocumentController struct {
	store   repo.Store
	storage providers.ObjectStorage
	now     func() time.Time
}

func NewTier3DocumentController(store repo.Store, storage providers.ObjectStorage) *Tier3DocumentController {
	return &Tier3DocumentController{store: store, storage: storage, now: time.Now}
}

func (c *Tier3DocumentController) Upload(ctx context.Context, userID uuid.UUID, in UploadDocumentInput) (*models.Tier3Document, error) {
	if c.storage == nil {
		return nil, messages.ErrProviderUnavailable
	}
	if !allowedDocumentType(in.DocumentType) || !allowedDocumentContentType(in.ContentType) || in.IssueDate.IsZero() || len(in.Data) == 0 || int64(len(in.Data)) > messages.MaxTier3DocumentBytes {
		return nil, messages.ErrInvalidDocument
	}
	profile, err := c.store.KYC().ProfileByUserID(ctx, userID)
	if err != nil || profile.Tier < models.KYCTier2 {
		return nil, messages.ErrBVNRequiredFirst
	}
	filename := safeFilename(in.Filename)
	if filename == "" || !extensionMatchesContentType(strings.ToLower(filepath.Ext(filename)), in.ContentType) {
		return nil, messages.ErrInvalidDocument
	}
	if !contentSignatureMatches(in.Data, in.ContentType) {
		return nil, messages.ErrInvalidDocument
	}
	now, documentID := c.now().UTC(), uuid.New()
	key := fmt.Sprintf("tier3/%s/%s/%s", userID, documentID, filename)
	metadata, err := c.storage.Upload(ctx, key, in.ContentType, in.Data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(in.Data)
	doc := &models.Tier3Document{ID: documentID, UserID: userID, DocumentType: in.DocumentType, OriginalFilename: filename, IssueDate: in.IssueDate, ObjectKey: key, ContentType: in.ContentType, SizeBytes: metadata.Size, SHA256: hex.EncodeToString(sum[:]), ETag: metadata.ETag, Status: models.ComponentSubmitted, UploadedAt: &now, CreatedAt: now}
	err = c.store.Atomic(ctx, func(tx repo.Store) error {
		application, findErr := tx.Tier3().ApplicationByUserID(ctx, userID)
		if errors.Is(findErr, repo.ErrNotFound) {
			application = &models.Tier3Application{UserID: userID, Status: models.Tier3InProgress, NINStatus: models.ComponentMissing, AddressStatus: models.ComponentMissing, LocationStatus: models.ComponentMissing, UtilityDocumentStatus: models.ComponentSubmitted, CountryCode: "NG", CreatedAt: now, UpdatedAt: now}
			if err := tx.Tier3().CreateApplication(ctx, application); err != nil {
				return err
			}
		} else if findErr != nil {
			return findErr
		}
		doc.ApplicationID = application.ID
		if err := tx.Tier3().CreateDocument(ctx, doc); err != nil {
			return err
		}
		application.UtilityDocumentStatus, application.UpdatedAt = models.ComponentSubmitted, now
		return tx.Tier3().UpdateApplication(ctx, application)
	})
	if err != nil {
		_ = c.storage.Delete(ctx, key)
		return nil, err
	}
	return doc, nil
}

type DownloadedDocument struct {
	Data                  []byte
	Filename, ContentType string
}

func (c *Tier3DocumentController) Download(ctx context.Context, userID, documentID uuid.UUID) (*DownloadedDocument, error) {
	doc, err := c.store.Tier3().DocumentByID(ctx, userID, documentID)
	if err != nil || doc.UploadedAt == nil {
		return nil, messages.ErrDocumentNotFound
	}
	data, err := c.storage.Download(ctx, doc.ObjectKey)
	if err != nil {
		return nil, err
	}
	return &DownloadedDocument{Data: data, Filename: doc.OriginalFilename, ContentType: doc.ContentType}, nil
}
func allowedDocumentType(v string) bool { return v == "utility_bill" || v == "bank_statement" }
func allowedDocumentContentType(v string) bool {
	return v == "application/pdf" || v == "image/png" || v == "image/jpeg"
}
func extensionMatchesContentType(ext, mime string) bool {
	return ext == ".pdf" && mime == "application/pdf" || ext == ".png" && mime == "image/png" || (ext == ".jpg" || ext == ".jpeg") && mime == "image/jpeg"
}
func contentSignatureMatches(data []byte, mime string) bool {
	if mime == "application/pdf" {
		return len(data) >= 5 && string(data[:5]) == "%PDF-"
	}
	if mime == "image/png" {
		return len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n"
	}
	return len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff
}

func safeFilename(v string) string {
	return strings.Trim(messages.UnsafeFilenamePattern.ReplaceAllString(filepath.Base(strings.TrimSpace(v)), "_"), "._")
}
