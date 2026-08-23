package controllers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	models "nabla/identity-svc/internal/models"
	repo "nabla/identity-svc/internal/repository"
)

type RequestContext struct {
	ActorType  models.AuditActorType
	ActorID    *uuid.UUID
	RequestID  string
	IPAddress  string
	UserAgent  string
	DeviceID   string
	DeviceName string
	DeviceIP   string
}

type requestContextKey struct{}

func WithRequestContext(ctx context.Context, value RequestContext) context.Context {
	if value.ActorType == "" {
		value.ActorType = models.AuditActorSystem
	}
	return context.WithValue(ctx, requestContextKey{}, value)
}

func RequestContextFrom(ctx context.Context) RequestContext {
	value := models.MetadataFromContext(ctx)
	return RequestContext{ActorType: value.ActorType, ActorID: value.ActorID, RequestID: value.RequestID, IPAddress: value.IPAddress, UserAgent: value.UserAgent, DeviceID: value.DeviceID, DeviceName: value.DeviceName, DeviceIP: value.DeviceIP}
}

type AuditEntry struct {
	Action     string
	EntityType string
	EntityID   *uuid.UUID
	Before     any
	After      any
}

type AuditController interface {
	Record(ctx context.Context, store repo.Store, entry AuditEntry) error
	Search(ctx context.Context, filter repo.AuditFilter) ([]models.AuditLog, int64, error)
}

type auditController struct {
	store repo.Store
	now   func() time.Time
}

func NewAuditController(store repo.Store) AuditController {
	return &auditController{store: store, now: time.Now}
}

func (c *auditController) Record(ctx context.Context, store repo.Store, entry AuditEntry) error {
	if store == nil {
		store = c.store
	}
	meta := RequestContextFrom(ctx)

	log := &models.AuditLog{
		ActorType:  meta.ActorType,
		ActorID:    meta.ActorID,
		Action:     entry.Action,
		EntityType: entry.EntityType,
		EntityID:   entry.EntityID,
		RequestID:  meta.RequestID,
		IPAddress:  meta.IPAddress,
		UserAgent:  truncate(meta.UserAgent, 500),
		CreatedAt:  c.now().UTC(),
	}

	before, err := marshalState(entry.Before)
	if err != nil {
		return err
	}
	after, err := marshalState(entry.After)
	if err != nil {
		return err
	}
	log.BeforeState = before
	log.AfterState = after

	return store.Audit().Record(ctx, log)
}

func (c *auditController) Search(ctx context.Context, filter repo.AuditFilter) ([]models.AuditLog, int64, error) {
	return c.store.Audit().Search(ctx, filter)
}

func marshalState(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
