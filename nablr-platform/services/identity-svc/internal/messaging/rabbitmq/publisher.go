package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/rabbitmq/amqp091-go"

	"nabla/identity-svc/internal/controllers"
)

// Publisher sends committed outbox events to durable RabbitMQ topic exchanges.
// It connects lazily so a broker outage never prevents identity-svc from
// starting; the outbox relay leaves failed events in PostgreSQL for retry.
type Publisher struct {
	url string

	mu      sync.Mutex
	conn    *amqp091.Connection
	channel *amqp091.Channel
}

func NewPublisher(url string) *Publisher {
	return &Publisher{url: url}
}

func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, envelope controllers.EventEnvelope) error {
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	channel, err := p.openChannel()
	if err != nil {
		return err
	}
	if err := channel.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		p.reset()
		return fmt.Errorf("declare exchange %q: %w", exchange, err)
	}

	confirmation, err := channel.PublishWithDeferredConfirmWithContext(
		ctx,
		exchange,
		routingKey,
		false,
		false,
		amqp091.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp091.Persistent,
			MessageId:    envelope.ID.String(),
			Type:         envelope.Type,
			Timestamp:    envelope.OccurredAt,
			Body:         body,
		},
	)
	if err != nil {
		p.reset()
		return fmt.Errorf("publish event: %w", err)
	}
	if confirmation == nil {
		return errors.New("publish event: broker confirmation unavailable")
	}
	acknowledged, err := confirmation.WaitContext(ctx)
	if err != nil {
		p.reset()
		return fmt.Errorf("wait for broker confirmation: %w", err)
	}
	if !acknowledged {
		return errors.New("publish event: broker rejected message")
	}
	return nil
}

func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var err error
	if p.channel != nil {
		err = p.channel.Close()
	}
	if p.conn != nil {
		if closeErr := p.conn.Close(); err == nil {
			err = closeErr
		}
	}
	p.channel = nil
	p.conn = nil
	return err
}

func (p *Publisher) openChannel() (*amqp091.Channel, error) {
	if p.channel != nil && !p.channel.IsClosed() && p.conn != nil && !p.conn.IsClosed() {
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
		return nil, fmt.Errorf("open RabbitMQ channel: %w", err)
	}
	if err := channel.Confirm(false); err != nil {
		_ = channel.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("enable RabbitMQ publisher confirms: %w", err)
	}

	p.conn = conn
	p.channel = channel
	return channel, nil
}

func (p *Publisher) reset() {
	if p.channel != nil {
		_ = p.channel.Close()
	}
	if p.conn != nil {
		_ = p.conn.Close()
	}
	p.channel = nil
	p.conn = nil
}

var _ controllers.EventPublisher = (*Publisher)(nil)
