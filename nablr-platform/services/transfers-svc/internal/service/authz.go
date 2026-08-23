package service

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/models"
)

const (
	limitPerTransaction  = "per_transaction"
	limitDailyOutbound   = "daily_outbound"
	limitBalanceCap      = "balance_cap"
	limitWeeklyOutbound  = "weekly_outbound"
	limitMonthlyOutbound = "monthly_outbound"
	limitDailyCount      = "daily_count"
)

const noBalanceCap int64 = -1

type tierCeilings struct {
	perTransactionMinor int64
	dailyOutboundMinor  int64
	balanceCapMinor     int64 // noBalanceCap => no active balance_cap row
}

var cbnTierLadder = map[int32]tierCeilings{
	1: {perTransactionMinor: 50_000_00, dailyOutboundMinor: 50_000_00, balanceCapMinor: 300_000_00},
	2: {perTransactionMinor: 200_000_00, dailyOutboundMinor: 500_000_00, balanceCapMinor: 5_000_000_00},
	3: {perTransactionMinor: 5_000_000_00, dailyOutboundMinor: 10_000_000_00, balanceCapMinor: noBalanceCap},
}

type authorized struct {
	tier int32
	pep  bool
}

func (s *Service) authorize(ctx context.Context, u uuid.UUID, in TransferInput) (authorized, error) {
	if s.identity == nil {
		log.Printf("transfer authorization failed step=identity_client user_id=%s error=%v", u, models.ErrAuthorizationUnavailable)
		return authorized{}, models.ErrAuthorizationUnavailable
	}

	if !in.PreAuthorized {
		if strings.TrimSpace(in.PIN) == "" {
			return authorized{}, models.ErrPINRequired
		}
		ok, err := s.identity.VerifyPIN(ctx, u, in.PIN)
		if err != nil {
			log.Printf("transfer authorization failed step=verify_pin user_id=%s error=%v", u, err)
			return authorized{}, authzError(err)
		}
		if !ok {
			return authorized{}, models.ErrPINInvalid
		}
	}

	user, err := s.identity.GetUser(ctx, u)
	if err != nil {
		log.Printf("transfer authorization failed step=get_user user_id=%s error=%v", u, err)
		return authorized{}, authzError(err)
	}
	if user.Status != "active" {
		log.Printf("transfer authorization blocked step=get_user user_id=%s status=%s error=%v", u, user.Status, models.ErrAccountNotActive)
		return authorized{}, models.ErrAccountNotActive
	}

	kyc, err := s.identity.GetKYCProfile(ctx, u)
	if err != nil {
		log.Printf("transfer authorization failed step=get_kyc_profile user_id=%s error=%v", u, err)
		return authorized{}, authzError(err)
	}

	if kyc.Sanctioned {
		return authorized{}, models.ErrSanctioned
	}

	if _, ok := cbnTierLadder[kyc.Tier]; !ok {
		return authorized{}, models.ErrTierLimitExceeded
	}
	return authorized{tier: kyc.Tier, pep: kyc.PEP}, nil
}

func authzError(err error) error {
	log.Printf("[authzError] identity error: %v", err)

	switch {
	case errors.Is(err, ErrIdentityUnavailable):
		return errors.Join(models.ErrAuthorizationUnavailable, err)
	case errors.Is(err, ErrIdentityUserNotFound):
		return models.ErrAccountNotActive
	default:
		return err
	}
}

func materializeTierLimits(ctx context.Context, q *db.Queries, u uuid.UUID, cur string, tier int32) error {
	c, ok := cbnTierLadder[tier]
	if !ok {
		// authorize already refused an off-ladder tier; nothing to materialise.
		return nil
	}
	if err := q.UpsertTierLimit(ctx, db.UpsertTierLimitParams{
		UserID: u, LimitType: limitPerTransaction, Currency: cur,
		AmountMinor: pgtype.Int8{Int64: c.perTransactionMinor, Valid: true},
	}); err != nil {
		return err
	}
	if err := q.UpsertTierLimit(ctx, db.UpsertTierLimitParams{
		UserID: u, LimitType: limitDailyOutbound, Currency: cur,
		AmountMinor: pgtype.Int8{Int64: c.dailyOutboundMinor, Valid: true},
	}); err != nil {
		return err
	}
	if c.balanceCapMinor == noBalanceCap {
		return q.DeleteTierLimit(ctx, db.DeleteTierLimitParams{UserID: u, LimitType: limitBalanceCap, Currency: cur})
	}
	return q.UpsertTierLimit(ctx, db.UpsertTierLimitParams{
		UserID: u, LimitType: limitBalanceCap, Currency: cur,
		AmountMinor: pgtype.Int8{Int64: c.balanceCapMinor, Valid: true},
	})
}

func activeAmountLimit(ctx context.Context, q *db.Queries, u uuid.UUID, cur, limitType string) (int64, bool, error) {
	row, err := q.ActiveLimit(ctx, db.ActiveLimitParams{UserID: u, Currency: cur, LimitType: limitType})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if !row.AmountMinor.Valid {
		return 0, false, nil
	}
	return row.AmountMinor.Int64, true, nil
}
