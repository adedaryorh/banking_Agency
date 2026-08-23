package controllers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	models "nabla/identity-svc/internal/models"
	repo "nabla/identity-svc/internal/repository"
)

type EventEnvelope struct {
	ID            uuid.UUID       `json:"event_id"`
	Type          string          `json:"event_type"`
	Version       int             `json:"event_version"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   uuid.UUID       `json:"aggregate_id"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Producer      string          `json:"producer"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type EventPublisher interface {
	Publish(ctx context.Context, exchange, routingKey string, envelope EventEnvelope) error
}

type OutboxController interface {
	Emit(ctx context.Context, store repo.Store, event OutboxMessage) error
	PendingCount(ctx context.Context) (int64, error)
}

type OutboxMessage struct {
	Type          string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       any
	RoutingKey    string
	AvailableAt   time.Time
}

type outboxController struct {
	store repo.Store
	now   func() time.Time
}

func NewOutboxController(store repo.Store) OutboxController {
	return &outboxController{store: store, now: time.Now}
}

func (c *outboxController) Emit(ctx context.Context, store repo.Store, message OutboxMessage) error {
	if message.Type == "" {
		return messages.ErrOutboxEventTypeRequired
	}
	if store == nil {
		store = c.store
	}

	payload := json.RawMessage([]byte("{}"))
	if message.Payload != nil {
		raw, err := json.Marshal(message.Payload)
		if err != nil {
			return err
		}
		payload = json.RawMessage(raw)
	}

	routingKey := message.RoutingKey
	if routingKey == "" {
		routingKey = message.Type
	}
	availableAt := message.AvailableAt
	if availableAt.IsZero() {
		availableAt = c.now().UTC()
	}

	return store.Outbox().Enqueue(ctx, &models.OutboxEvent{
		Exchange:      messages.EventExchange,
		RoutingKey:    routingKey,
		EventType:     message.Type,
		AggregateType: message.AggregateType,
		AggregateID:   message.AggregateID,
		Payload:       payload,
		Status:        models.OutboxStatusPending,
		AvailableAt:   availableAt,
	})
}

func (c *outboxController) PendingCount(ctx context.Context) (int64, error) {
	return c.store.Outbox().PendingCount(ctx)
}

type OutboxRelayConfig struct {
	BatchSize             int
	PollInterval          time.Duration
	MaxAttempts           int
	BaseBackoff           time.Duration
	MaxBackoff            time.Duration
	RetentionAfterPublish time.Duration
}

func (c OutboxRelayConfig) withDefaults() OutboxRelayConfig {
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 12
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = 2 * time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 10 * time.Minute
	}
	if c.RetentionAfterPublish <= 0 {
		c.RetentionAfterPublish = 7 * 24 * time.Hour
	}
	return c
}

type OutboxRelay struct {
	store     repo.Store
	publisher EventPublisher
	config    OutboxRelayConfig
	now       func() time.Time
}

func NewOutboxRelay(store repo.Store, publisher EventPublisher, config OutboxRelayConfig) *OutboxRelay {
	return &OutboxRelay{
		store:     store,
		publisher: publisher,
		config:    config.withDefaults(),
		now:       time.Now,
	}
}

func (r *OutboxRelay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				count, err := r.DrainOnce(ctx)
				if err != nil || count < r.config.BatchSize {
					break
				}
			}
		}
	}
}

func (r *OutboxRelay) DrainOnce(ctx context.Context) (int, error) {
	handled := 0

	err := r.store.Atomic(ctx, func(store repo.Store) error {
		events, err := store.Outbox().ClaimBatch(ctx, r.config.BatchSize, r.now().UTC())
		if err != nil {
			return err
		}
		handled = len(events)

		for i := range events {
			event := events[i]
			envelope := EventEnvelope{
				ID:            event.ID,
				Type:          event.EventType,
				Version:       1,
				AggregateType: event.AggregateType,
				AggregateID:   event.AggregateID,
				OccurredAt:    event.CreatedAt,
				Producer:      "identity-svc",
				CorrelationID: event.ID.String(),
				Payload:       json.RawMessage(event.Payload),
			}

			publishErr := r.publisher.Publish(ctx, event.Exchange, event.RoutingKey, envelope)
			if publishErr == nil {
				if err := store.Outbox().MarkPublished(ctx, event.ID, r.now().UTC()); err != nil {
					return err
				}
				continue
			}
			if event.Attempts+1 >= r.config.MaxAttempts {
				if err := store.Outbox().MarkDeadLetter(ctx, event.ID, publishErr.Error()); err != nil {
					return err
				}
				continue
			}

			delay := helpers.BackoffDelay(event.Attempts+1, r.config.BaseBackoff, r.config.MaxBackoff)
			if err := store.Outbox().MarkFailed(ctx, event.ID, publishErr.Error(), r.now().UTC().Add(delay)); err != nil {
				return err
			}
		}
		return nil
	})

	return handled, err
}

func (r *OutboxRelay) Prune(ctx context.Context) (int64, error) {
	cutoff := r.now().UTC().Add(-r.config.RetentionAfterPublish)
	return r.store.Outbox().DeletePublishedBefore(ctx, cutoff)
}
