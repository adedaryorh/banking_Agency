package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"

	notificationdb "nabla/notification-svc/db/sqlc"
)

const maxAttempts int32 = 12

type Envelope struct {
	EventID       uuid.UUID       `json:"event_id"`
	EventType     string          `json:"event_type"`
	EventVersion  int             `json:"event_version"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   *uuid.UUID      `json:"aggregate_id,omitempty"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Producer      string          `json:"producer"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type Relay struct {
	queries   *notificationdb.Queries
	publisher *publisher
}

func NewRelay(pool *pgxpool.Pool, url string) *Relay {
	return &Relay{queries: notificationdb.New(pool), publisher: &publisher{url: url}}
}

func (r *Relay) Close() error { return r.publisher.Close() }

func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.drain(ctx); err != nil {
				log.Printf("notification outbox relay: %v", err)
			}
		}
	}
}

func (r *Relay) drain(ctx context.Context) error {
	events, err := r.queries.ClaimUnpublishedNotificationEvents(ctx, 100)
	if err != nil {
		return err
	}
	for _, event := range events {
		envelope := Envelope{EventID: event.ID, EventType: event.EventType, EventVersion: 1, AggregateType: "notification_delivery", AggregateID: event.AggregateID, OccurredAt: event.CreatedAt, Producer: "notification-svc", Payload: event.Payload}
		envelope.CorrelationID = eventCorrelationID(event.Payload, event.ID)
		if err := r.publisher.Publish(ctx, "nabla.events", event.EventType, envelope); err != nil {
			failure := pgtype.Text{String: err.Error(), Valid: true}
			if event.Attempts >= maxAttempts {
				_ = r.queries.DeadLetterNotificationEvent(ctx, notificationdb.DeadLetterNotificationEventParams{ID: event.ID, LastError: failure})
				continue
			}
			_ = r.queries.MarkNotificationEventFailed(ctx, notificationdb.MarkNotificationEventFailedParams{ID: event.ID, LastError: failure, AvailableAt: time.Now().UTC().Add(backoff(event.Attempts))})
			continue
		}
		_, _ = r.queries.MarkNotificationEventPublished(ctx, event.ID)
	}
	return nil
}

func eventCorrelationID(payload json.RawMessage, eventID uuid.UUID) string {
	var metadata struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(payload, &metadata) == nil && metadata.RequestID != "" {
		return metadata.RequestID
	}
	return eventID.String()
}

func backoff(attempts int32) time.Duration {
	delay := time.Duration(attempts) * 5 * time.Second
	if delay < 5*time.Second {
		return 5 * time.Second
	}
	if delay > 10*time.Minute {
		return 10 * time.Minute
	}
	return delay
}

type publisher struct {
	url     string
	mu      sync.Mutex
	conn    *amqp091.Connection
	channel *amqp091.Channel
}

func (p *publisher) Publish(ctx context.Context, exchange, key string, envelope Envelope) error {
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	channel, err := p.open()
	if err != nil {
		return err
	}
	if err = channel.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		p.reset()
		return err
	}
	confirmation, err := channel.PublishWithDeferredConfirmWithContext(ctx, exchange, key, false, false, amqp091.Publishing{ContentType: "application/json", DeliveryMode: amqp091.Persistent, MessageId: envelope.EventID.String(), Type: envelope.EventType, Timestamp: envelope.OccurredAt, Body: body})
	if err != nil {
		p.reset()
		return err
	}
	if confirmation == nil {
		return errors.New("RabbitMQ confirmation unavailable")
	}
	ok, err := confirmation.WaitContext(ctx)
	if err != nil {
		p.reset()
		return err
	}
	if !ok {
		return errors.New("RabbitMQ rejected event")
	}
	return nil
}

func (p *publisher) open() (*amqp091.Channel, error) {
	if p.conn != nil && !p.conn.IsClosed() && p.channel != nil && !p.channel.IsClosed() {
		return p.channel, nil
	}
	p.reset()
	conn, err := amqp091.Dial(p.url)
	if err != nil {
		return nil, fmt.Errorf("connect RabbitMQ: %w", err)
	}
	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err = channel.Confirm(false); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		return nil, err
	}
	p.conn, p.channel = conn, channel
	return channel, nil
}

func (p *publisher) Close() error { p.mu.Lock(); defer p.mu.Unlock(); p.reset(); return nil }
func (p *publisher) reset() {
	if p.channel != nil {
		_ = p.channel.Close()
	}
	if p.conn != nil {
		_ = p.conn.Close()
	}
	p.channel, p.conn = nil, nil
}
