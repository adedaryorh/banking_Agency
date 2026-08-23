package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event represents an outbox event for reliable message delivery
type Event struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	Payload       map[string]string
	RequestID     string
	CreatedAt     time.Time
	ProcessedAt   *time.Time
}

// Enqueue adds an event to the outbox for later processing
func Enqueue(ctx context.Context, db interface{}, aggregateType string, aggregateID uuid.UUID,
	eventType string, payload map[string]string, requestID string) error {

	eventID := uuid.New()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox: marshal payload: %w", err)
	}

	now := time.Now()

	// Support both pgxpool.Pool and pgx.Tx
	switch d := db.(type) {
	case *pgxpool.Pool:
		_, err = d.Exec(ctx, `
			INSERT INTO outbox_events
				(id, aggregate_type, aggregate_id, event_type, payload, request_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			eventID, aggregateType, aggregateID, eventType, payloadJSON, requestID, now)
	case pgx.Tx:
		_, err = d.Exec(ctx, `
			INSERT INTO outbox_events
				(id, aggregate_type, aggregate_id, event_type, payload, request_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			eventID, aggregateType, aggregateID, eventType, payloadJSON, requestID, now)
	default:
		return fmt.Errorf("outbox: unsupported db type")
	}

	return err
}

// Processor handles outbox event processing
type Processor struct {
	pool    *pgxpool.Pool
	handler func(ctx context.Context, event Event) error
}

// NewProcessor creates a new outbox processor
func NewProcessor(pool *pgxpool.Pool, handler func(context.Context, Event) error) *Processor {
	return &Processor{
		pool:    pool,
		handler: handler,
	}
}

// ProcessBatch processes a batch of pending outbox events
func (p *Processor) ProcessBatch(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 100
	}

	// Select and lock unprocessed events
	rows, err := p.pool.Query(ctx, `
		SELECT id, aggregate_type, aggregate_id, event_type, payload, request_id, created_at
		FROM outbox_events
		WHERE processed_at IS NULL
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, batchSize)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var payloadJSON []byte
		err := rows.Scan(&e.ID, &e.AggregateType, &e.AggregateID, &e.EventType,
			&payloadJSON, &e.RequestID, &e.CreatedAt)
		if err != nil {
			return 0, err
		}

		if err := json.Unmarshal(payloadJSON, &e.Payload); err != nil {
			return 0, err
		}

		events = append(events, e)
	}

	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Process each event
	processed := 0
	for _, event := range events {
		if err := p.handler(ctx, event); err != nil {
			// Log error but continue processing other events
			continue
		}

		// Mark as processed
		_, err := p.pool.Exec(ctx, `
			UPDATE outbox_events
			SET processed_at = $2
			WHERE id = $1`, event.ID, time.Now())
		if err != nil {
			continue
		}

		processed++
	}

	return processed, nil
}

// PurgeProcessed removes old processed events to prevent table bloat
func (p *Processor) PurgeProcessed(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)

	tag, err := p.pool.Exec(ctx, `
		DELETE FROM outbox_events
		WHERE processed_at IS NOT NULL AND processed_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}

	return tag.RowsAffected(), nil
}
