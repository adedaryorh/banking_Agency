package controllers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	models "nabla/identity-svc/internal/models"
	repo "nabla/identity-svc/internal/repository"
)

type SecurityController interface {
	SetPIN(ctx context.Context, userID uuid.UUID, pin string) error
	ChangePIN(ctx context.Context, userID uuid.UUID, currentPIN, newPIN string) error
	ResetPIN(ctx context.Context, userID uuid.UUID, challengeToken, code, newPIN string) error
	VerifyPIN(ctx context.Context, store repo.Store, userID uuid.UUID, pin string) error
	RequiresPIN(amountMinor int64) bool
	HasPIN(ctx context.Context, userID uuid.UUID) (bool, error)

	RegisterDevice(ctx context.Context, userID uuid.UUID, input DeviceInput) (*models.UserDevice, bool, error)
	ListDevices(ctx context.Context, userID uuid.UUID) ([]models.UserDevice, error)
	TrustDevice(ctx context.Context, userID, deviceID uuid.UUID, challengeToken, code string) (*models.UserDevice, error)
	BlockDevice(ctx context.Context, userID, deviceID uuid.UUID) error
}

type DeviceInput struct {
	Fingerprint string
	Name        string
	Platform    string
	PushToken   string
	AppVersion  string
	OSVersion   string
	DeviceModel string
	IPAddress   string
	Latitude    *float64
	Longitude   *float64
}

type securityController struct {
	store             repo.Store
	otp               OTPController
	audit             AuditController
	outbox            OutboxController
	pinThresholdMinor int64
	now               func() time.Time
}

func NewSecurityController(
	store repo.Store,
	otp OTPController,
	audit AuditController,
	outbox OutboxController,
	pinThresholdMinor int64,
) SecurityController {
	if pinThresholdMinor < 0 {
		pinThresholdMinor = 0
	}
	return &securityController{
		store:             store,
		otp:               otp,
		audit:             audit,
		outbox:            outbox,
		pinThresholdMinor: pinThresholdMinor,
		now:               time.Now,
	}
}

func (c *securityController) SetPIN(ctx context.Context, userID uuid.UUID, pin string) error {
	if err := validatePIN(pin); err != nil {
		return err
	}

	hash, err := helpers.Hash(pin)
	if err != nil {
		return err
	}
	now := c.now().UTC()

	return c.store.Atomic(ctx, func(store repo.Store) error {
		if _, err := store.Security().PINByUserID(ctx, userID); err == nil {
			return messages.ErrPINAlreadySet
		} else if !errors.Is(err, repo.ErrNotFound) {
			return err
		}

		record := &models.TransactionPIN{
			UserID:        userID,
			PINHash:       hash,
			LastChangedAt: now,
		}
		if err := store.Security().CreatePIN(ctx, record); err != nil {
			if errors.Is(err, repo.ErrConflict) {
				return messages.ErrPINAlreadySet
			}
			return err
		}

		return c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionPINSet,
			EntityType: "transaction_pin",
			EntityID:   &record.ID,
		})
	})
}

func (c *securityController) ChangePIN(ctx context.Context, userID uuid.UUID, currentPIN, newPIN string) error {
	if err := validatePIN(newPIN); err != nil {
		return err
	}
	if currentPIN == newPIN {
		return messages.ErrPINSameAsCurrent
	}

	if err := c.VerifyPIN(ctx, nil, userID, currentPIN); err != nil {
		return err
	}

	hash, err := helpers.Hash(newPIN)
	if err != nil {
		return err
	}
	now := c.now().UTC()

	return c.store.Atomic(ctx, func(store repo.Store) error {
		if err := store.Security().UpdatePINHash(ctx, userID, hash, now); err != nil {
			return err
		}
		return c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionPINChanged,
			EntityType: "transaction_pin",
		})
	})
}

func (c *securityController) ResetPIN(ctx context.Context, userID uuid.UUID, challengeToken, code, newPIN string) error {
	if err := validatePIN(newPIN); err != nil {
		return err
	}

	hash, err := helpers.Hash(newPIN)
	if err != nil {
		return err
	}
	now := c.now().UTC()

	return c.store.Atomic(ctx, func(store repo.Store) error {
		if _, err := c.otp.Verify(ctx, store, challengeToken, code, models.OTPPurposePINReset); err != nil {
			return err
		}

		_, err := store.Security().PINByUserID(ctx, userID)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			record := &models.TransactionPIN{UserID: userID, PINHash: hash, LastChangedAt: now}
			if err := store.Security().CreatePIN(ctx, record); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if err := store.Security().UpdatePINHash(ctx, userID, hash, now); err != nil {
				return err
			}
		}

		return c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionPINChanged,
			EntityType: "transaction_pin",
			After:      map[string]any{"method": "otp_reset"},
		})
	})
}

func (c *securityController) VerifyPIN(ctx context.Context, store repo.Store, userID uuid.UUID, pin string) error {
	if store == nil {
		store = c.store
	}
	now := c.now().UTC()

	record, err := store.Security().LockPINByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return messages.ErrPINNotSet
		}
		return err
	}
	if record.IsLocked(now) {
		return messages.ErrPINLocked
	}

	if !helpers.Matches(record.PINHash, pin) {
		var lockedUntil *time.Time
		attempts := record.FailedAttempts + 1
		if attempts >= messages.PINMaxAttempts {
			until := now.Add(messages.PINLockWindow)
			lockedUntil = &until
		}
		if failErr := c.store.Security().RecordPINFailure(ctx, record.ID, lockedUntil); failErr != nil {
			return failErr
		}

		if lockedUntil != nil {
			_ = c.store.Atomic(ctx, func(inner repo.Store) error {
				if err := c.audit.Record(ctx, inner, AuditEntry{
					Action:     messages.AuditActionPINLocked,
					EntityType: "transaction_pin",
					EntityID:   &record.ID,
					After:      map[string]any{"locked_until": lockedUntil},
				}); err != nil {
					return err
				}
				return c.outbox.Emit(ctx, inner, OutboxMessage{
					Type:          messages.EventPINLocked,
					AggregateType: messages.AggregateUser,
					AggregateID:   userID,
					Payload: map[string]any{
						"user_id":      userID,
						"locked_until": lockedUntil,
					},
				})
			})
			return messages.ErrPINLocked
		}

		_ = c.store.Atomic(ctx, func(inner repo.Store) error {
			return c.audit.Record(ctx, inner, AuditEntry{
				Action:     messages.AuditActionPINFailed,
				EntityType: "transaction_pin",
				EntityID:   &record.ID,
				After:      map[string]any{"failed_attempts": attempts},
			})
		})
		return messages.ErrPINInvalid
	}

	return store.Security().ResetPINAttempts(ctx, record.ID, now)
}

func (c *securityController) RequiresPIN(amountMinor int64) bool {
	if c.pinThresholdMinor <= 0 {
		return true
	}
	return amountMinor >= c.pinThresholdMinor
}

func (c *securityController) HasPIN(ctx context.Context, userID uuid.UUID) (bool, error) {
	if _, err := c.store.Security().PINByUserID(ctx, userID); err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *securityController) RegisterDevice(ctx context.Context, userID uuid.UUID, input DeviceInput) (*models.UserDevice, bool, error) {
	fingerprint := strings.TrimSpace(input.Fingerprint)
	if fingerprint == "" {
		return nil, false, messages.ErrDeviceNotFound
	}
	now := c.now().UTC()

	existing, err := c.store.Security().DeviceByFingerprint(ctx, userID, fingerprint)
	if err == nil {
		if existing.Blocked {
			return existing, false, messages.ErrDeviceBlocked
		}
		existing.Name = firstNonEmpty(input.Name, existing.Name)
		existing.Platform = firstNonEmpty(input.Platform, existing.Platform)
		existing.PushToken = firstNonEmpty(input.PushToken, existing.PushToken)
		existing.AppVersion = firstNonEmpty(input.AppVersion, existing.AppVersion)
		existing.OSVersion = firstNonEmpty(input.OSVersion, existing.OSVersion)
		existing.DeviceModel = firstNonEmpty(input.DeviceModel, existing.DeviceModel)
		existing.LastIP = input.IPAddress
		existing.Latitude = input.Latitude
		existing.Longitude = input.Longitude
		existing.LastSeenAt = &now
		if err := c.store.Security().UpsertDevice(ctx, existing); err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, repo.ErrNotFound) {
		return nil, false, err
	}

	device := &models.UserDevice{
		UserID:      userID,
		Fingerprint: fingerprint,
		Name:        input.Name,
		Platform:    input.Platform,
		PushToken:   input.PushToken,
		AppVersion:  input.AppVersion,
		OSVersion:   input.OSVersion,
		DeviceModel: input.DeviceModel,
		LastIP:      input.IPAddress,
		Latitude:    input.Latitude,
		Longitude:   input.Longitude,
		LastSeenAt:  &now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	err = c.store.Atomic(ctx, func(store repo.Store) error {
		if err := store.Security().UpsertDevice(ctx, device); err != nil {
			return err
		}
		if err := c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionDeviceRegistered,
			EntityType: "user_device",
			EntityID:   &device.ID,
			After: map[string]any{
				"platform":  device.Platform,
				"latitude":  device.Latitude,
				"longitude": device.Longitude,
			},
		}); err != nil {
			return err
		}
		return c.outbox.Emit(ctx, store, OutboxMessage{
			Type:          messages.EventDeviceRegistered,
			AggregateType: messages.AggregateUser,
			AggregateID:   userID,
			Payload: map[string]any{
				"user_id":   userID,
				"device_id": device.ID,
				"name":      device.Name,
				"platform":  device.Platform,
				"latitude":  device.Latitude,
				"longitude": device.Longitude,
			},
		})
	})
	if err != nil {
		return nil, false, err
	}
	return device, true, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (c *securityController) ListDevices(ctx context.Context, userID uuid.UUID) ([]models.UserDevice, error) {
	return c.store.Security().DevicesByUserID(ctx, userID)
}

func (c *securityController) TrustDevice(ctx context.Context, userID, deviceID uuid.UUID, challengeToken, code string) (*models.UserDevice, error) {
	device, err := c.store.Security().DeviceByID(ctx, deviceID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, messages.ErrDeviceNotFound
		}
		return nil, err
	}
	if device.UserID != userID {
		return nil, messages.ErrDeviceNotFound
	}
	if device.Blocked {
		return nil, messages.ErrDeviceBlocked
	}

	now := c.now().UTC()
	err = c.store.Atomic(ctx, func(store repo.Store) error {
		if _, err := c.otp.Verify(ctx, store, challengeToken, code, models.OTPPurposeNewDevice); err != nil {
			return err
		}
		if err := store.Security().SetDeviceTrusted(ctx, device.ID, true, now); err != nil {
			return err
		}
		return c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionDeviceTrusted,
			EntityType: "user_device",
			EntityID:   &device.ID,
		})
	})
	if err != nil {
		return nil, err
	}

	device.Trusted = true
	device.TrustedAt = &now
	return device, nil
}

func (c *securityController) BlockDevice(ctx context.Context, userID, deviceID uuid.UUID) error {
	device, err := c.store.Security().DeviceByID(ctx, deviceID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return messages.ErrDeviceNotFound
		}
		return err
	}
	if device.UserID != userID {
		return messages.ErrDeviceNotFound
	}

	return c.store.Atomic(ctx, func(store repo.Store) error {
		if err := store.Security().SetDeviceBlocked(ctx, device.ID, true); err != nil {
			return err
		}
		return c.audit.Record(ctx, store, AuditEntry{
			Action:     messages.AuditActionDeviceBlocked,
			EntityType: "user_device",
			EntityID:   &device.ID,
		})
	})
}

func validatePIN(pin string) error {
	if len(pin) < messages.PINMinLength || len(pin) > messages.PINMaxLength {
		return messages.ErrPINWeak
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return messages.ErrPINWeak
		}
	}

	allSame := true
	ascending := true
	descending := true
	for i := 1; i < len(pin); i++ {
		if pin[i] != pin[0] {
			allSame = false
		}
		if pin[i] != pin[i-1]+1 {
			ascending = false
		}
		if pin[i] != pin[i-1]-1 {
			descending = false
		}
	}
	if allSame || ascending || descending {
		return messages.ErrPINWeak
	}
	return nil
}
