package service

import (
	"context"
	"encoding/json"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/metrics"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type PayoutWebhook struct {
	EventID        string `json:"event_id"`
	Event          string `json:"event"`
	TransferID     string `json:"transfer_id"`
	ProviderRef    string `json:"provider_reference"`
	OurRef         string `json:"our_reference"`
	Status         string `json:"status"`
	FailureCode    string `json:"failure_code,omitempty"`
	FailureMessage string `json:"failure_reason,omitempty"`
	Timestamp      string `json:"timestamp"`
	Notify         string `json:"notify"`
	NotifyType     string `json:"notifyType"`
	Data           struct {
		ID            string `json:"id"`
		Reference     string `json:"reference"`
		Status        string `json:"status"`
		SessionID     string `json:"sessionid"`
		FailureCode   string `json:"failure_code,omitempty"`
		FailureReason string `json:"failure_reason,omitempty"`
	} `json:"data"`
}

func (p PayoutWebhook) Reference() string {
	if p.Data.Reference != "" {
		return p.Data.Reference
	}
	if p.ProviderRef != "" {
		return p.ProviderRef
	}
	if p.OurRef != "" {
		return p.OurRef
	}
	return p.TransferID
}

// FailureReason falls back from the provider's richer fields to a generic note.
func (p PayoutWebhook) FailureReason(fallback string) string {
	if p.Data.FailureReason != "" {
		return p.Data.FailureReason
	}
	if p.Data.FailureCode != "" {
		return p.Data.FailureCode
	}
	if p.FailureMessage != "" {
		return p.FailureMessage
	}
	if p.FailureCode != "" {
		return p.FailureCode
	}
	return fallback
}

func (p PayoutWebhook) OutcomeFields() (string, string) {
	event := p.Event
	if event == "" {
		event = p.Notify
	}
	status := p.Status
	if status == "" {
		status = p.Data.Status
	}
	if status == "" {
		status = p.NotifyType
	}
	return event, status
}

// PayoutOutcome is the set of moves that are terminal for money.
type PayoutOutcome int

const (
	PayoutSettled PayoutOutcome = iota + 1
	PayoutFailed
)

func ClassifyPayout(event, status string) (PayoutOutcome, bool) {
	for _, v := range []string{event, status} {
		switch v {
		case "payout.settled", "payout.completed", "payout.successful",
			"settled", "completed", "successful", "success":
			return PayoutSettled, true
		case "payout.failed", "payout.returned", "payout.reversed", "payout.rejected",
			"failed", "returned", "reversed", "rejected", "declined":
			return PayoutFailed, true
		}
	}
	return 0, false
}

// WebhookReplayResult describes one admin replay attempt.
type WebhookReplayResult struct {
	WebhookID uuid.UUID
	// Status is one of: processed | failed | unreadable | no_reference | not_handled
	Status    string
	Applied   bool
	Reference string
}

func (s *Service) ReplayWebhook(ctx context.Context, id, replayedBy uuid.UUID) (*WebhookReplayResult, error) {
	wh, err := s.q.ProviderWebhookByID(ctx, id)
	if err != nil {
		return nil, err
	}

	var p PayoutWebhook
	if err := json.Unmarshal(wh.Payload, &p); err != nil {
		return &WebhookReplayResult{WebhookID: id, Status: "unreadable"}, nil
	}
	ref := p.Reference()
	if ref == "" {
		return &WebhookReplayResult{WebhookID: id, Status: "no_reference"}, nil
	}
	outcome, known := ClassifyPayout(p.Event, p.Status)
	if !known {
		return &WebhookReplayResult{WebhookID: id, Status: "not_handled", Reference: ref}, nil
	}

	result := &WebhookReplayResult{WebhookID: id, Reference: ref}
	switch outcome {
	case PayoutSettled:
		if e := s.SettlePayoutByReference(ctx, ref); e != nil {
			return result, e
		}
		result.Status = "processed"
		result.Applied = true
	case PayoutFailed:
		if e := s.FailPayoutByReference(ctx, ref, p.FailureReason("webhook replay")); e != nil {
			return result, e
		}
		result.Status = "processed"
		result.Applied = true
	}

	_ = s.q.MarkWebhookReplayed(ctx, db.MarkWebhookReplayedParams{
		ID:         id,
		ReplayedBy: pgtype.UUID{Bytes: replayedBy, Valid: replayedBy != uuid.Nil},
	})
	metrics.WebhooksReplayed.Inc()
	return result, nil
}

// ProviderWebhooks lists the most recent captured callbacks for the ops screen.
func (s *Service) ProviderWebhooks(ctx context.Context, limit int32) ([]db.ProviderWebhook, error) {
	if limit < 1 {
		limit = 25
	}
	return s.q.ListProviderWebhooks(ctx, limit)
}
