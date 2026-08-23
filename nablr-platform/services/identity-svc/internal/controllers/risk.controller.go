package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"nabla/identity-svc/internal/common/messages"
	models "nabla/identity-svc/internal/models"
	repo "nabla/identity-svc/internal/repository"
)

type RiskLevel string

type RiskAssessmentInput struct {
	UserID      uuid.UUID
	AccountID   uuid.UUID
	DeviceID    *uuid.UUID
	IPAddress   string
	AmountMinor int64
	Currency    string
	Operation   string
	Recipient   string
}

type RiskAssessmentResult struct {
	Level        RiskLevel
	Allowed      bool
	Challenge    bool
	Reason       string
	RiskScore    int
	AssessmentID uuid.UUID
}

type RiskEngine interface {
	Assess(ctx context.Context, input RiskAssessmentInput) (*RiskAssessmentResult, error)

	RecordOutcome(ctx context.Context, assessmentID uuid.UUID, passed bool)

	GetDeviceRisk(ctx context.Context, userID, deviceID uuid.UUID) (int, error)

	ListRecentAssessments(ctx context.Context, userID uuid.UUID, limit int) ([]models.RiskAssessment, error)
}

type riskEngine struct {
	store  repo.Store
	audit  AuditController
	outbox OutboxController
	now    func() time.Time
}

type RiskEngineConfig struct {
	Store  repo.Store
	Audit  AuditController
	Outbox OutboxController
}

func NewRiskEngine(config RiskEngineConfig) RiskEngine {
	return &riskEngine{
		store:  config.Store,
		audit:  config.Audit,
		outbox: config.Outbox,
		now:    time.Now,
	}
}

func (e *riskEngine) Assess(ctx context.Context, input RiskAssessmentInput) (*RiskAssessmentResult, error) {
	now := e.now().UTC()

	assessment := &models.RiskAssessment{
		UserID:      input.UserID,
		Reference:   input.AccountID.String(),
		DeviceID:    input.DeviceID,
		Operation:   input.Operation,
		AmountMinor: input.AmountMinor,
		Currency:    input.Currency,
		IPAddress:   input.IPAddress,
		CreatedAt:   now,
	}

	result := &RiskAssessmentResult{
		Allowed: true,
		Level:   messages.RiskLevelLow,
	}

	if blocked, reason := e.checkVelocity(ctx, input); blocked {
		assessment.Decision = "blocked"
		assessment.TriggeredRules = reason
		assessment.Score = 100
		assessment.CreatedAt = now

		_ = e.store.Security().RecordAssessment(ctx, assessment)

		_ = e.audit.Record(ctx, e.store, AuditEntry{
			Action:     messages.AuditActionRiskBlocked,
			EntityType: "risk_assessment",
			EntityID:   &assessment.ID,
			After:      assessment,
		})

		_ = e.outbox.Emit(ctx, e.store, OutboxMessage{
			Type:          messages.EventRiskBlocked,
			AggregateType: messages.AggregateUser,
			AggregateID:   input.UserID,
			Payload: map[string]any{
				"user_id":      input.UserID,
				"amount_minor": input.AmountMinor,
				"operation":    input.Operation,
				"reason":       reason,
			},
		})

		return &RiskAssessmentResult{
			Level:        messages.RiskLevelBlocked,
			Allowed:      false,
			Reason:       reason,
			RiskScore:    100,
			AssessmentID: assessment.ID,
		}, nil
	}

	if input.DeviceID != nil && input.AmountMinor > 5000000 {
		if blocked, reason := e.checkDevice(ctx, input); blocked {
			assessment.Decision = "challenge"
			assessment.TriggeredRules = reason
			assessment.Score = 60
			assessment.CreatedAt = now

			_ = e.store.Security().RecordAssessment(ctx, assessment)

			_ = e.audit.Record(ctx, e.store, AuditEntry{
				Action:     messages.AuditActionRiskChallenged,
				EntityType: "risk_assessment",
				EntityID:   &assessment.ID,
				After:      assessment,
			})

			return &RiskAssessmentResult{
				Level:        messages.RiskLevelHigh,
				Allowed:      false,
				Challenge:    true,
				Reason:       reason,
				RiskScore:    60,
				AssessmentID: assessment.ID,
			}, nil
		}
	}

	if input.AmountMinor > 10000000 {
		if blocked, reason := e.checkAnomaly(ctx, input); blocked {
			assessment.Decision = "challenge"
			assessment.TriggeredRules = reason
			assessment.Score = 40
			assessment.CreatedAt = now

			_ = e.store.Security().RecordAssessment(ctx, assessment)

			_ = e.audit.Record(ctx, e.store, AuditEntry{
				Action:     messages.AuditActionRiskChallenged,
				EntityType: "risk_assessment",
				EntityID:   &assessment.ID,
				After:      assessment,
			})

			return &RiskAssessmentResult{
				Level:        messages.RiskLevelMedium,
				Allowed:      false,
				Challenge:    true,
				Reason:       reason,
				RiskScore:    40,
				AssessmentID: assessment.ID,
			}, nil
		}
	}

	assessment.Decision = "allowed"
	assessment.Score = 0
	assessment.CreatedAt = now
	_ = e.store.Security().RecordAssessment(ctx, assessment)

	result.AssessmentID = assessment.ID
	return result, nil
}

func (e *riskEngine) checkVelocity(ctx context.Context, input RiskAssessmentInput) (bool, string) {
	recent, err := e.store.Security().AssessmentsByUserID(ctx, input.UserID, 10)
	if err != nil {
		return false, ""
	}

	if len(recent) >= 5 {
		return true, messages.ErrRiskVelocityExceeded.Error()
	}

	var totalMinor int64
	for _, a := range recent {
		totalMinor += a.AmountMinor
	}
	if totalMinor > input.AmountMinor*5 {
		return true, messages.ErrRiskVelocityExceeded.Error()
	}

	return false, ""
}

func (e *riskEngine) checkDevice(ctx context.Context, input RiskAssessmentInput) (bool, string) {
	if input.DeviceID == nil {
		return true, messages.ErrRiskNewDevice.Error()
	}

	device, err := e.store.Security().DeviceByID(ctx, *input.DeviceID)
	if err != nil {
		return true, messages.ErrRiskNewDevice.Error()
	}

	if device.UserID != input.UserID {
		return true, messages.ErrRiskDeviceNotTrusted.Error()
	}

	if device.Blocked {
		return true, messages.ErrRiskDeviceNotTrusted.Error()
	}

	if !device.Trusted {
		return true, messages.ErrRiskDeviceNotTrusted.Error()
	}

	return false, ""
}

func (e *riskEngine) checkAnomaly(ctx context.Context, input RiskAssessmentInput) (bool, string) {
	recent, err := e.store.Security().AssessmentsByUserID(ctx, input.UserID, 50)
	if err != nil || len(recent) < 3 {
		return false, ""
	}

	var totalMinor int64
	for _, a := range recent {
		totalMinor += a.AmountMinor
	}
	avg := totalMinor / int64(len(recent))

	if input.AmountMinor > avg*10 {
		return true, fmt.Sprintf("%s: your average is %.2f NGN", messages.ErrRiskAnomalousAmount.Error(), float64(avg)/100)
	}

	return false, ""
}

func (e *riskEngine) RecordOutcome(ctx context.Context, assessmentID uuid.UUID, passed bool) {
	_ = e.audit.Record(ctx, e.store, AuditEntry{
		Action:     "risk.challenge_answered",
		EntityType: "risk_assessment",
		EntityID:   &assessmentID,
		After: map[string]any{
			"passed": passed,
		},
	})
}

func (e *riskEngine) GetDeviceRisk(ctx context.Context, userID, deviceID uuid.UUID) (int, error) {
	device, err := e.store.Security().DeviceByID(ctx, deviceID)
	if err != nil {
		return 100, nil
	}
	if device.UserID != userID {
		return 100, nil
	}
	if device.Blocked {
		return 100, nil
	}
	if device.Trusted {
		return 0, nil
	}
	return 30, nil
}

func (e *riskEngine) ListRecentAssessments(ctx context.Context, userID uuid.UUID, limit int) ([]models.RiskAssessment, error) {
	return e.store.Security().AssessmentsByUserID(ctx, userID, limit)
}
