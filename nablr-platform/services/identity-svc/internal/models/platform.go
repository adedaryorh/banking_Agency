package models

import (
	"context"
	"time"

	"encoding/json"

	"github.com/google/uuid"
)

type IdempotencyStatus string

const (
	IdempotencyStatusInFlight IdempotencyStatus = "in_flight"
	IdempotencyStatusComplete IdempotencyStatus = "complete"
	IdempotencyStatusFailed   IdempotencyStatus = "failed"
)

type IdempotencyKey struct {
	ID          uuid.UUID         `json:"id"`
	Key         string            `json:"key"`
	UserID      uuid.UUID         `json:"user_id"`
	Endpoint    string            `json:"endpoint"`
	RequestHash string            `json:"request_hash"`
	Status      IdempotencyStatus `json:"status"`

	ResponseStatus int             `json:"response_status"`
	ResponseBody   json.RawMessage `json:"response_body,omitempty"`

	ResourceID *uuid.UUID `json:"resource_id,omitempty"`

	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (IdempotencyKey) TableName() string { return "idempotency_keys" }

type OutboxStatus string

const (
	OutboxStatusPending    OutboxStatus = "pending"
	OutboxStatusPublished  OutboxStatus = "published"
	OutboxStatusFailed     OutboxStatus = "failed"
	OutboxStatusDeadLetter OutboxStatus = "dead_letter"
)

type OutboxEvent struct {
	ID         uuid.UUID `json:"id"`
	Exchange   string    `json:"exchange"`
	RoutingKey string    `json:"routing_key"`
	EventType  string    `json:"event_type"`

	AggregateType string    `json:"aggregate_type"`
	AggregateID   uuid.UUID `json:"aggregate_id"`

	Payload json.RawMessage `json:"payload"`

	Status      OutboxStatus `json:"status"`
	Attempts    int          `json:"attempts"`
	LastError   string       `json:"last_error,omitempty"`
	AvailableAt time.Time    `json:"available_at"`
	PublishedAt *time.Time   `json:"published_at,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

func (OutboxEvent) TableName() string { return "outbox_events" }

type AuditActorType string

const (
	AuditActorUser     AuditActorType = "user"
	AuditActorAdmin    AuditActorType = "admin"
	AuditActorSystem   AuditActorType = "system"
	AuditActorProvider AuditActorType = "provider"
)

type RequestMetadata struct {
	ActorType  AuditActorType
	ActorID    *uuid.UUID
	RequestID  string
	IPAddress  string
	UserAgent  string
	DeviceID   string
	DeviceName string
	DeviceIP   string
}

type metadataKey struct{}

func MetadataWithContext(ctx context.Context, metadata RequestMetadata) context.Context {
	return context.WithValue(ctx, metadataKey{}, metadata)
}

func MetadataFromContext(ctx context.Context) RequestMetadata {
	metadata, _ := ctx.Value(metadataKey{}).(RequestMetadata)
	return metadata
}

type AuditLog struct {
	ID         uuid.UUID      `json:"id"`
	ActorType  AuditActorType `json:"actor_type"`
	ActorID    *uuid.UUID     `json:"actor_id,omitempty"`
	Action     string         `json:"action"`
	EntityType string         `json:"entity_type"`
	EntityID   *uuid.UUID     `json:"entity_id,omitempty"`

	BeforeState json.RawMessage `json:"before_state,omitempty"`
	AfterState  json.RawMessage `json:"after_state,omitempty"`

	RequestID string    `json:"request_id,omitempty"`
	IPAddress string    `json:"ip_address,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_logs" }
