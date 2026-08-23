package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	db "nabla/transfers-svc/db/sqlc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

const notificationMaxAttempts = 8

// notificationKind enumerates the money events a customer is told about.
type notificationKind string

const (
	notifyDebit  notificationKind = "debit"  // money left the sender's wallet
	notifyCredit notificationKind = "credit" // money arrived in the recipient's wallet
	notifyFailed notificationKind = "failed" // an attempted transfer did not go through
)

type notification struct {
	UserID      uuid.UUID        `json:"user_id"`
	Kind        notificationKind `json:"kind"`
	AmountMinor int64            `json:"amount_minor"`
	Currency    string           `json:"currency"`
	Reference   string           `json:"reference"`
	Reason      string           `json:"reason,omitempty"`
}

func enqueueNotification(ctx context.Context, q *db.Queries, aggregateID uuid.UUID, n notification) error {
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	_, err = q.CreateOutboxEvent(ctx, db.CreateOutboxEventParams{
		AggregateType: "transfer",
		AggregateID:   aggregateID,
		EventType:     "notification.transfer." + string(n.Kind),
		Payload:       payload,
	})
	return err
}

func (s *Service) dispatchNotification(ctx context.Context, event db.OutboxEvent) {
	if s.notifier == nil || s.identity == nil {
		// This deployment has no notification transport. Dropping is the honest
		// outcome — there is nowhere to send — and it must not wedge the queue.
		s.dropNotification(ctx, event, "no notification transport configured")
		return
	}
	var n notification
	if err := json.Unmarshal(event.Payload, &n); err != nil || n.UserID == uuid.Nil {
		s.dropNotification(ctx, event, "unparseable notification payload")
		return
	}
	if event.Attempts >= notificationMaxAttempts {
		log.Printf("transfers: giving up on notification %s for %s after %d attempts", event.EventType, n.UserID, event.Attempts)
		_ = s.q.MarkOutboxPublished(ctx, event.ID)
		return
	}

	rcpt, err := s.resolveRecipient(ctx, n.UserID)
	if err != nil {
		if errors.Is(err, ErrIdentityUserNotFound) {
			s.dropNotification(ctx, event, "recipient no longer exists at identity")
			return
		}
		// Transient or unclassified identity failure: retry later.
		s.retryNotification(ctx, event, "resolve recipient: "+err.Error())
		return
	}

	subject, emailText := renderNotification(n, rcpt)
	attempted, delivered := false, false
	// All transfer alerts are email-only. This does not affect authentication
	// and onboarding SMS, which are produced by identity-svc independently.
	if rcpt.Email != "" {
		attempted = true
		if ok, err := s.notifier.SendEmail(ctx, rcpt.Email, subject, emailText, ""); err != nil {
			log.Printf("transfers: notification email for %s failed: %v", n.UserID, err)
		} else if ok {
			delivered = true
		}
	}

	switch {
	case !attempted:
		// No usable contact on file; retrying cannot invent one.
		s.dropNotification(ctx, event, "recipient has no phone or email on file")
	case delivered:
		// Email accepted the alert.
		_ = s.q.MarkOutboxPublished(ctx, event.ID)
	default:
		s.retryNotification(ctx, event, "email delivery failed")
	}
}

// dropNotification abandons an event that can never succeed, logging why.
func (s *Service) dropNotification(ctx context.Context, event db.OutboxEvent, why string) {
	log.Printf("transfers: dropping notification %s: %s", event.EventType, why)
	_ = s.q.MarkOutboxPublished(ctx, event.ID)
}

// retryNotification defers an event with linear backoff (attempts × 2m, floored
// at 1m, capped at 30m) so a flapping provider or a briefly-down identity is
// re-tried without hammering either.
func (s *Service) retryNotification(ctx context.Context, event db.OutboxEvent, why string) {
	backoff := time.Duration(event.Attempts) * 2 * time.Minute
	if backoff < time.Minute {
		backoff = time.Minute
	}
	if backoff > 30*time.Minute {
		backoff = 30 * time.Minute
	}
	_ = s.q.MarkOutboxFailed(ctx, db.MarkOutboxFailedParams{
		ID:          event.ID,
		LastError:   pgtype.Text{String: why, Valid: true},
		AvailableAt: time.Now().Add(backoff),
	})
}

// recipient is a customer's delivery details, assembled from identity.
type recipient struct {
	Name  string
	Phone string
	Email string
}

// resolveRecipient reads the email identity used by transfer alerts. Transfer
// notifications are email-only, so delivery must not depend on KYC or phone
// profile availability.
func (s *Service) resolveRecipient(ctx context.Context, userID uuid.UUID) (recipient, error) {
	u, err := s.identity.GetUser(ctx, userID)
	if err != nil {
		return recipient{}, err
	}
	return recipient{
		Phone: u.PhoneNumber,
		Email: u.Email,
	}, nil
}

// renderNotification builds the subject and email text for a transfer alert.
func renderNotification(n notification, r recipient) (subject, email string) {
	amount := formatMoney(n.AmountMinor, n.Currency)
	greeting := "Hello"
	if r.Name != "" {
		greeting = "Hello " + r.Name
	}
	switch n.Kind {
	case notifyCredit:
		subject = "Credit Alert: " + amount
		email = fmt.Sprintf("%s,\n\nYou have received %s.\nReference: %s", greeting, amount, n.Reference)
	case notifyFailed:
		reason := n.Reason
		if reason == "" {
			reason = "Your money has not been taken."
		}
		subject = "Transfer Failed: " + amount
		email = fmt.Sprintf("%s,\n\nYour transfer of %s could not be completed.\n%s\nReference: %s", greeting, amount, reason, n.Reference)
	default: // notifyDebit
		subject = "Debit Alert: " + amount
		email = fmt.Sprintf("%s,\n\nYour transfer of %s has been sent.\nReference: %s\n\nIf you did not authorise this, contact support immediately.", greeting, amount, n.Reference)
	}
	return subject, email
}

// formatMoney renders integer minor units as major units with two decimals and
// thousands separators, e.g. (500000, "NGN") -> "NGN 5,000.00". Pure integer
// math: money is never routed through a float. Assumes a 2-decimal currency,
// which every currency this service handles is.
func formatMoney(minor int64, currency string) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	digits := fmt.Sprintf("%d", minor/100)
	var grouped strings.Builder
	for i, ch := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(ch)
	}
	cur := currency
	if cur == "" {
		cur = "NGN"
	}
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%s %s.%02d", sign, cur, grouped.String(), minor%100)
}
