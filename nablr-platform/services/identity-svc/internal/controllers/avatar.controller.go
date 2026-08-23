package controllers

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"nabla/identity-svc/internal/common/messages"
	"nabla/identity-svc/internal/models"
	"nabla/identity-svc/internal/providers"
	repo "nabla/identity-svc/internal/repository"

	"github.com/google/uuid"
)

type AvatarUploadInput struct {
	Filename, ContentType string
	Data                  []byte
}

type DownloadedImage struct {
	Data                  []byte
	Filename, ContentType string
}

// AvatarController manages a user's own profile image: upload a fresh object to
// storage and point the user row at it, or fetch the bytes back. Images are
// stored under avatars/<user>/<uuid>.<ext> and the storage key is persisted on
// users.avatar_url so the rest of the platform can resolve it without a DB trip
// to this service.
type AvatarController struct {
	store   repo.Store
	storage providers.ObjectStorage
	now     func() time.Time
}

func NewAvatarController(store repo.Store, storage providers.ObjectStorage) *AvatarController {
	return &AvatarController{store: store, storage: storage, now: time.Now}
}

func (c *AvatarController) Upload(ctx context.Context, userID uuid.UUID, in AvatarUploadInput) (*models.User, error) {
	if c.storage == nil {
		return nil, messages.ErrProviderUnavailable
	}
	if !allowedAvatarContentType(in.ContentType) || len(in.Data) == 0 || int64(len(in.Data)) > messages.MaxUploadImageBytes {
		return nil, messages.ErrInvalidImage
	}
	if detected := http.DetectContentType(in.Data); detected != in.ContentType {
		return nil, messages.ErrInvalidImage
	}
	filename := safeFilename(in.Filename)
	ext := strings.ToLower(filepath.Ext(filename))
	if filename == "" || !extensionMatchesContentType(ext, in.ContentType) {
		return nil, messages.ErrInvalidImage
	}
	if !contentSignatureMatches(in.Data, in.ContentType) {
		return nil, messages.ErrInvalidImage
	}
	key := fmt.Sprintf("avatars/%s/%s%s", userID, uuid.New(), ext)
	if _, err := c.storage.Upload(ctx, key, in.ContentType, in.Data); err != nil {
		return nil, err
	}
	user, err := c.store.Auth().UpdateUserAvatar(ctx, userID, key)
	if err != nil {
		_ = c.storage.Delete(ctx, key)
		return nil, err
	}
	return user, nil
}

func (c *AvatarController) Download(ctx context.Context, userID uuid.UUID) (*DownloadedImage, error) {
	if c.storage == nil {
		return nil, messages.ErrProviderUnavailable
	}
	user, err := c.store.Auth().UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.AvatarURL == "" {
		return nil, messages.ErrAvatarNotFound
	}
	data, err := c.storage.Download(ctx, user.AvatarURL)
	if err != nil {
		return nil, err
	}
	contentType := http.DetectContentType(data)
	return &DownloadedImage{Data: data, Filename: filepath.Base(user.AvatarURL), ContentType: contentType}, nil
}

func allowedAvatarContentType(v string) bool {
	return v == "image/png" || v == "image/jpeg"
}