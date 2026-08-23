package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rabbitmq/amqp091-go"

	"nabla/transfers-svc/internal/observability"
	"nabla/transfers-svc/internal/service"
)

const (
	identityQueue            = "transfers.identity-events.v1"
	identityRetryQueue       = "transfers.identity-events.v1.retry"
	identityDeadQueue        = "transfers.identity-events.v1.dead"
	consumerName             = "transfers.identity-provisioning.v1"
	maxDeliveries      int64 = 12
)

type Envelope struct {
	EventID       uuid.UUID       `json:"event_id"`
	EventType     string          `json:"event_type"`
	EventVersion  int             `json:"event_version"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   uuid.UUID       `json:"aggregate_id"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Producer      string          `json:"producer"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type onboardingPayload struct {
	UserID   uuid.UUID `json:"user_id"`
	Currency string    `json:"currency"`
}

type IdentityConsumer struct {
	url     string
	db      *pgxpool.Pool
	wallets *service.WalletService
	funding *service.FundingService
}

type DeadLetter struct {
	ID           uuid.UUID `json:"id"`
	EventID      uuid.UUID `json:"event_id"`
	EventType    string    `json:"event_type"`
	ErrorMessage string    `json:"error_message"`
	Attempts     int       `json:"attempts"`
	CreatedAt    time.Time `json:"created_at"`
}

func NewIdentityConsumer(url string, db *pgxpool.Pool, wallets *service.WalletService, funding *service.FundingService) *IdentityConsumer {
	return &IdentityConsumer{url: url, db: db, wallets: wallets, funding: funding}
}

func (c *IdentityConsumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.consume(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("identity event consumer: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *IdentityConsumer) consume(ctx context.Context) error {
	conn, err := amqp091.Dial(c.url)
	if err != nil {
		return fmt.Errorf("connect RabbitMQ: %w", err)
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		return err
	}
	defer channel.Close()
	if err = declareTopology(channel); err != nil {
		return err
	}
	if err = channel.Qos(8, 0, false); err != nil {
		return err
	}
	deliveries, err := channel.Consume(identityQueue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("RabbitMQ delivery channel closed")
			}
			c.handle(ctx, channel, delivery)
		}
	}
}

func declareTopology(ch *amqp091.Channel) error {
	for _, exchange := range []struct{ name, kind string }{
		{"nabla.events", "topic"}, {"nabla.retry", "direct"}, {"nabla.dead-letter", "topic"},
	} {
		if err := ch.ExchangeDeclare(exchange.name, exchange.kind, true, false, false, false, nil); err != nil {
			return err
		}
	}
	if _, err := ch.QueueDeclare(identityQueue, true, false, false, false, amqp091.Table{"x-dead-letter-exchange": "nabla.retry", "x-dead-letter-routing-key": identityQueue}); err != nil {
		return err
	}
	if err := ch.QueueBind(identityQueue, "identity.onboarding.completed", "nabla.events", false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(identityRetryQueue, true, false, false, false, amqp091.Table{"x-message-ttl": int32(30000), "x-dead-letter-exchange": "nabla.events", "x-dead-letter-routing-key": "identity.onboarding.completed"}); err != nil {
		return err
	}
	if err := ch.QueueBind(identityRetryQueue, identityQueue, "nabla.retry", false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(identityDeadQueue, true, false, false, false, nil); err != nil {
		return err
	}
	return ch.QueueBind(identityDeadQueue, "identity.onboarding.completed", "nabla.dead-letter", false, nil)
}

func (c *IdentityConsumer) handle(ctx context.Context, channel *amqp091.Channel, delivery amqp091.Delivery) {
	var envelope Envelope
	if err := json.Unmarshal(delivery.Body, &envelope); err != nil || envelope.EventID == uuid.Nil {
		_ = c.deadLetter(ctx, channel, delivery, envelope, "invalid event envelope", deliveryCount(delivery))
		return
	}
	correlationID := envelope.CorrelationID
	if correlationID == "" {
		correlationID = envelope.EventID.String()
	}
	ctx = observability.WithCorrelationID(ctx, correlationID)
	completed, err := c.alreadyCompleted(ctx, envelope.EventID)
	if err != nil {
		_ = delivery.Nack(false, false)
		return
	}
	if completed {
		_ = delivery.Ack(false)
		return
	}

	var payload onboardingPayload
	if err = json.Unmarshal(envelope.Payload, &payload); err != nil {
		_ = c.deadLetter(ctx, channel, delivery, envelope, "invalid onboarding payload", deliveryCount(delivery))
		return
	}
	if payload.UserID == uuid.Nil {
		payload.UserID = envelope.AggregateID
	}
	if payload.Currency == "" {
		payload.Currency = "NGN"
	}

	_, _ = c.db.Exec(ctx, `INSERT INTO consumed_events(consumer_name,event_id,event_type,status) VALUES($1,$2,$3,'processing') ON CONFLICT(consumer_name,event_id) DO UPDATE SET status='processing',attempts=consumed_events.attempts+1,last_error=NULL`, consumerName, envelope.EventID, envelope.EventType)
	if _, err = c.wallets.OpenWallet(ctx, payload.UserID, payload.Currency, "Main wallet"); err == nil {
		if c.funding == nil {
			err = service.ErrCollectionsNotConfigured
		} else {
			_, err = c.funding.EnsureAccount(ctx, payload.UserID)
		}
	}
	if err == nil {
		_, _ = c.db.Exec(ctx, `UPDATE consumed_events SET status='completed',processed_at=now(),last_error=NULL WHERE consumer_name=$1 AND event_id=$2`, consumerName, envelope.EventID)
		_ = delivery.Ack(false)
		return
	}
	_, _ = c.db.Exec(ctx, `UPDATE consumed_events SET status='failed',last_error=$3 WHERE consumer_name=$1 AND event_id=$2`, consumerName, envelope.EventID, err.Error())
	count := deliveryCount(delivery)
	if count >= maxDeliveries {
		_ = c.deadLetter(ctx, channel, delivery, envelope, err.Error(), count)
		return
	}
	_ = delivery.Nack(false, false)
}

func (c *IdentityConsumer) alreadyCompleted(ctx context.Context, eventID uuid.UUID) (bool, error) {
	var status string
	err := c.db.QueryRow(ctx, `SELECT status FROM consumed_events WHERE consumer_name=$1 AND event_id=$2`, consumerName, eventID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return status == "completed", err
}

func (c *IdentityConsumer) deadLetter(ctx context.Context, channel *amqp091.Channel, delivery amqp091.Delivery, envelope Envelope, reason string, attempts int64) error {
	if envelope.EventID != uuid.Nil {
		_, _ = c.db.Exec(ctx, `INSERT INTO dead_letter_events(consumer_name,event_id,event_type,payload,error_message,attempts) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(consumer_name,event_id) DO UPDATE SET error_message=EXCLUDED.error_message,attempts=EXCLUDED.attempts`, consumerName, envelope.EventID, envelope.EventType, delivery.Body, reason, attempts)
	}
	err := channel.PublishWithContext(ctx, "nabla.dead-letter", "identity.onboarding.completed", false, false, amqp091.Publishing{ContentType: "application/json", DeliveryMode: amqp091.Persistent, MessageId: delivery.MessageId, Type: delivery.Type, Timestamp: time.Now().UTC(), Headers: amqp091.Table{"x-error": reason}, Body: delivery.Body})
	if err != nil {
		_ = delivery.Nack(false, true)
		return err
	}
	return delivery.Ack(false)
}

func deliveryCount(delivery amqp091.Delivery) int64 {
	var total int64 = 1
	deaths, ok := delivery.Headers["x-death"].([]interface{})
	if !ok {
		return total
	}
	for _, raw := range deaths {
		death, ok := raw.(amqp091.Table)
		if !ok {
			continue
		}
		switch count := death["count"].(type) {
		case int64:
			total += count
		case int32:
			total += int64(count)
		}
	}
	return total
}

func (c *IdentityConsumer) ReplayDeadLetter(ctx context.Context, id uuid.UUID) error {
	var eventType string
	var payload []byte
	err := c.db.QueryRow(ctx, `SELECT event_type,payload FROM dead_letter_events WHERE id=$1 AND replayed_at IS NULL`, id).Scan(&eventType, &payload)
	if err != nil {
		return err
	}
	conn, err := amqp091.Dial(c.url)
	if err != nil {
		return err
	}
	defer conn.Close()
	channel, err := conn.Channel()
	if err != nil {
		return err
	}
	defer channel.Close()
	if err = channel.ExchangeDeclare("nabla.events", "topic", true, false, false, false, nil); err != nil {
		return err
	}
	if err = channel.PublishWithContext(ctx, "nabla.events", eventType, false, false, amqp091.Publishing{ContentType: "application/json", DeliveryMode: amqp091.Persistent, Type: eventType, Timestamp: time.Now().UTC(), Body: payload}); err != nil {
		return err
	}
	_, err = c.db.Exec(ctx, `UPDATE dead_letter_events SET replayed_at=now() WHERE id=$1`, id)
	return err
}

func (c *IdentityConsumer) DeadLetters(ctx context.Context, limit int) ([]DeadLetter, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := c.db.Query(ctx, `SELECT id,event_id,event_type,error_message,attempts,created_at FROM dead_letter_events WHERE replayed_at IS NULL ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DeadLetter, 0)
	for rows.Next() {
		var item DeadLetter
		if err = rows.Scan(&item.ID, &item.EventID, &item.EventType, &item.ErrorMessage, &item.Attempts, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
