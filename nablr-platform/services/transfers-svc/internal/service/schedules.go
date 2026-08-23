package service

import (
	"context"
	"errors"
	"strings"
	"time"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *Service) GetSchedule(ctx context.Context, u, id uuid.UUID) (db.ScheduledPayment, error) {
	row, err := s.q.ScheduleByID(ctx, db.ScheduleByIDParams{ID: id, UserID: u})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ScheduledPayment{}, models.ErrScheduleNotFound
	}
	return row, err
}

func (s *Service) ScheduleRuns(ctx context.Context, u, id uuid.UUID, limit int32) ([]db.ScheduledPaymentRun, error) {
	if _, err := s.q.ScheduleByID(ctx, db.ScheduleByIDParams{ID: id, UserID: u}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrScheduleNotFound
		}
		return nil, err
	}
	if limit < 1 {
		limit = 25
	}
	return s.q.ScheduleRunsBySchedule(ctx, db.ScheduleRunsByScheduleParams{ScheduledPaymentID: id, Limit: limit})
}

func (s *Service) PauseSchedule(ctx context.Context, u, id uuid.UUID, reason string) error {
	existing, err := s.GetSchedule(ctx, u, id)
	if err != nil {
		return err
	}
	if existing.Status != "active" {
		return scheduleValidation("schedule_not_active", "status", "only an active scheduled payment can be paused")
	}
	n, err := s.q.PauseSchedule(ctx, db.PauseScheduleParams{
		ID: id, UserID: u, PauseReason: schedText(reason),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return scheduleValidation("schedule_state_changed", "status", "scheduled payment status changed; refresh and try again")
	}
	return nil
}

func (s *Service) ResumeSchedule(ctx context.Context, u, id uuid.UUID) error {
	existing, err := s.GetSchedule(ctx, u, id)
	if err != nil {
		return err
	}
	if existing.Status != "paused" {
		return scheduleValidation("schedule_not_paused", "status", "only a paused scheduled payment can be resumed")
	}
	n, err := s.q.ResumeSchedule(ctx, db.ResumeScheduleParams{ID: id, UserID: u})
	if err != nil {
		return err
	}
	if n == 0 {
		return scheduleValidation("schedule_state_changed", "status", "scheduled payment status changed; refresh and try again")
	}
	return nil
}

type ScheduleUpdate struct {
	Name           *string
	AmountMinor    *int64
	Narrative      *string
	NextRunAt      *time.Time
	Frequency      *string
	EndDate        *time.Time
	ClearEndDate   bool
	MaxOccurrences *int32
}

func (s *Service) UpdateSchedule(ctx context.Context, u, id uuid.UUID, in ScheduleUpdate) (db.ScheduledPayment, error) {
	existing, err := s.q.ScheduleByID(ctx, db.ScheduleByIDParams{ID: id, UserID: u})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ScheduledPayment{}, models.ErrScheduleNotFound
	}
	if err != nil {
		return db.ScheduledPayment{}, err
	}
	if existing.Status != "active" {
		return db.ScheduledPayment{}, scheduleValidation("schedule_not_active", "status", "only an active scheduled payment can be edited")
	}
	name := existing.PaymentName
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" {
			return db.ScheduledPayment{}, scheduleValidation("schedule_name_required", "name", "payment name is required")
		}
		if len(name) > 120 {
			return db.ScheduledPayment{}, scheduleValidation("schedule_name_too_long", "name", "payment name must not exceed 120 characters")
		}
	}
	amountMinor := existing.AmountMinor
	if in.AmountMinor != nil {
		if *in.AmountMinor <= 0 {
			return db.ScheduledPayment{}, scheduleValidation("invalid_amount", "amount", "amount must be greater than zero")
		}
		amountMinor = *in.AmountMinor
	}
	narrative := existing.Narrative
	if in.Narrative != nil {
		narrative = schedText(strings.TrimSpace(*in.Narrative))
	}
	next := existing.NextRunAt
	if in.NextRunAt != nil {
		if in.NextRunAt.IsZero() || in.NextRunAt.Before(time.Now().Add(-time.Minute)) {
			return db.ScheduledPayment{}, scheduleValidation("payment_date_in_past", "next_run_at", "next payment date must not be in the past")
		}
		next = *in.NextRunAt
	}
	frequency := existing.Frequency.String
	if in.Frequency != nil {
		frequency, err = normaliseScheduleFrequency(existing.ScheduleType, *in.Frequency)
		if err != nil {
			return db.ScheduledPayment{}, err
		}
	}
	endDate := existing.EndDate
	if in.ClearEndDate {
		endDate = pgtype.Timestamptz{}
	}
	if in.EndDate != nil {
		endDate = pgtype.Timestamptz{Time: *in.EndDate, Valid: true}
	}
	if endDate.Valid && endDate.Time.Before(next) {
		return db.ScheduledPayment{}, scheduleValidation("invalid_end_date", "end_date", "end date must be on or after the next payment date")
	}
	maxOccurrences := existing.MaxOccurrences
	if in.MaxOccurrences != nil {
		if *in.MaxOccurrences < 1 || *in.MaxOccurrences < existing.OccurrenceCount {
			return db.ScheduledPayment{}, scheduleValidation("invalid_max_occurrences", "max_occurrences", "max_occurrences must be positive and cannot be less than payments already made")
		}
		maxOccurrences = pgtype.Int4{Int32: *in.MaxOccurrences, Valid: true}
	}
	dayOfMonth := pgtype.Int2{Int16: int16(next.Day()), Valid: existing.ScheduleType == "recurring" && scheduleUsesDayOfMonth(frequency)}
	dayOfWeek := pgtype.Int2{Int16: int16(next.Weekday()), Valid: existing.ScheduleType == "recurring" && scheduleUsesDayOfWeek(frequency)}
	updated, err := s.q.UpdateSchedulePresentation(ctx, db.UpdateSchedulePresentationParams{
		ID: id, UserID: u, PaymentName: name, AmountMinor: amountMinor,
		Narrative: narrative, NextRunAt: next,
		Frequency:  pgtype.Text{String: frequency, Valid: frequency != ""},
		DayOfMonth: dayOfMonth, DayOfWeek: dayOfWeek, EndDate: endDate,
		MaxOccurrences: maxOccurrences,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ScheduledPayment{}, scheduleValidation("schedule_state_changed", "status", "scheduled payment status changed; refresh and try again")
	}
	return updated, err
}

func scheduleValidation(code, field, message string) error {
	return &models.ScheduleValidationError{Code: code, Field: field, Message: message}
}

func schedText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func nextScheduleRun(from time.Time, scheduleType, frequency string, anchorDay int) time.Time {
	if scheduleType == "one_off" {
		return from // completed anyway
	}
	switch frequency {
	case "daily", "ramadan_daily":
		return from.AddDate(0, 0, 1)
	case "every_friday":
		next := from.AddDate(0, 0, 1)
		for next.Weekday() != time.Friday {
			next = next.AddDate(0, 0, 1)
		}
		return next
	case "weekly":
		return from.AddDate(0, 0, 7)
	case "fortnightly":
		return from.AddDate(0, 0, 14)
	case "quarterly":
		return addMonthsClamped(from, 3, anchorDay)
	case "annually":
		return addMonthsClamped(from, 12, anchorDay)
	default: // monthly
		return addMonthsClamped(from, 1, anchorDay)
	}
}

func addMonthsClamped(from time.Time, months, anchorDay int) time.Time {
	if anchorDay < 1 || anchorDay > 31 {
		anchorDay = from.Day()
	}
	firstOfTarget := time.Date(from.Year(), from.Month()+time.Month(months), 1, from.Hour(), from.Minute(), from.Second(), from.Nanosecond(), from.Location())
	lastDay := time.Date(firstOfTarget.Year(), firstOfTarget.Month()+1, 0, from.Hour(), from.Minute(), from.Second(), from.Nanosecond(), from.Location()).Day()
	if anchorDay > lastDay {
		anchorDay = lastDay
	}
	return time.Date(firstOfTarget.Year(), firstOfTarget.Month(), anchorDay, from.Hour(), from.Minute(), from.Second(), from.Nanosecond(), from.Location())
}
