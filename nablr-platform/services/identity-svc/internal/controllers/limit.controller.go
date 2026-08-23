package controllers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"nabla/identity-svc/internal/common/helpers"
	"nabla/identity-svc/internal/common/messages"
	models "nabla/identity-svc/internal/models"
	repo "nabla/identity-svc/internal/repository"
)

type LimitReservation struct {
	UserID      uuid.UUID
	Currency    string
	AmountMinor int64
	DailyID     uuid.UUID
	MonthlyID   uuid.UUID
}

type LimitController interface {
	Reserve(ctx context.Context, store repo.Store, request LimitRequest) (*LimitReservation, error)
	Release(ctx context.Context, store repo.Store, reservation *LimitReservation) error
	Snapshot(ctx context.Context, userID uuid.UUID, currency string) (*LimitSnapshot, error)
	UpsertLimit(ctx context.Context, limit *models.TransactionLimit) error
	ListLimits(ctx context.Context) ([]models.TransactionLimit, error)
}

type LimitRequest struct {
	UserID                uuid.UUID
	Currency              string
	AmountMinor           int64
	MinimumTier           models.KYCTier
	ResultingBalanceMinor *int64
}

type LimitSnapshot struct {
	Tier                      models.KYCTier `json:"tier"`
	Currency                  string         `json:"currency"`
	SingleTransactionMaxMinor int64          `json:"single_transaction_max_minor"`
	DailyMaxMinor             int64          `json:"daily_max_minor"`
	DailyUsedMinor            int64          `json:"daily_used_minor"`
	DailyRemainingMinor       int64          `json:"daily_remaining_minor"`
	MonthlyMaxMinor           int64          `json:"monthly_max_minor"`
	MonthlyUsedMinor          int64          `json:"monthly_used_minor"`
	MonthlyRemainingMinor     int64          `json:"monthly_remaining_minor"`
	MaxBalanceMinor           int64          `json:"max_balance_minor"`
	DailyCountMax             int            `json:"daily_count_max"`
	DailyCountUsed            int            `json:"daily_count_used"`
}

type limitController struct {
	store repo.Store
	audit AuditController
	now   func() time.Time
}

func NewLimitController(store repo.Store, audit AuditController) LimitController {
	return &limitController{store: store, audit: audit, now: time.Now}
}

func (c *limitController) Reserve(ctx context.Context, store repo.Store, request LimitRequest) (*LimitReservation, error) {
	if store == nil {
		store = c.store
	}
	if err := helpers.ValidateAmountMinor(request.AmountMinor); err != nil {
		return nil, err
	}

	profile, err := store.KYC().ProfileByUserID(ctx, request.UserID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, messages.ErrTierTooLow
		}
		return nil, err
	}
	if profile.Sanctioned {
		return nil, messages.ErrAccountRestricted
	}
	if profile.Tier < request.MinimumTier {
		return nil, messages.ErrTierTooLow
	}

	limit, err := store.KYC().LimitForTier(ctx, profile.Tier, request.Currency)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, messages.ErrLimitsNotConfigured
		}
		return nil, err
	}

	if limit.SingleTransactionMaxMinor > 0 && request.AmountMinor > limit.SingleTransactionMaxMinor {
		return nil, messages.ErrSingleLimitExceeded
	}
	if request.ResultingBalanceMinor != nil && limit.MaxBalanceMinor > 0 &&
		*request.ResultingBalanceMinor > limit.MaxBalanceMinor {
		return nil, messages.ErrBalanceLimitExceeded
	}

	now := c.now()
	dayKey := helpers.DayKey(now)
	monthKey := helpers.MonthKey(now)

	daily, err := store.KYC().LockUsage(ctx, request.UserID, request.Currency, models.LimitWindowDaily, dayKey)
	if err != nil {
		return nil, err
	}
	monthly, err := store.KYC().LockUsage(ctx, request.UserID, request.Currency, models.LimitWindowMonthly, monthKey)
	if err != nil {
		return nil, err
	}

	if limit.DailyMaxMinor > 0 && daily.UsedMinor+request.AmountMinor > limit.DailyMaxMinor {
		return nil, messages.ErrDailyLimitExceeded
	}
	if limit.MonthlyMaxMinor > 0 && monthly.UsedMinor+request.AmountMinor > limit.MonthlyMaxMinor {
		return nil, messages.ErrMonthlyLimitExceeded
	}
	if limit.DailyCountMax > 0 && daily.UsedCount+1 > limit.DailyCountMax {
		return nil, messages.ErrDailyCountExceeded
	}

	if err := store.KYC().ApplyUsageDelta(ctx, daily.ID, request.AmountMinor, 1); err != nil {
		return nil, err
	}
	if err := store.KYC().ApplyUsageDelta(ctx, monthly.ID, request.AmountMinor, 1); err != nil {
		return nil, err
	}

	return &LimitReservation{
		UserID:      request.UserID,
		Currency:    request.Currency,
		AmountMinor: request.AmountMinor,
		DailyID:     daily.ID,
		MonthlyID:   monthly.ID,
	}, nil
}

func (c *limitController) Release(ctx context.Context, store repo.Store, reservation *LimitReservation) error {
	if reservation == nil {
		return nil
	}
	if store == nil {
		store = c.store
	}
	if err := store.KYC().ApplyUsageDelta(ctx, reservation.DailyID, -reservation.AmountMinor, -1); err != nil {
		return err
	}
	return store.KYC().ApplyUsageDelta(ctx, reservation.MonthlyID, -reservation.AmountMinor, -1)
}

func (c *limitController) Snapshot(ctx context.Context, userID uuid.UUID, currency string) (*LimitSnapshot, error) {
	profile, err := c.store.KYC().ProfileByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	limit, err := c.store.KYC().LimitForTier(ctx, profile.Tier, currency)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, messages.ErrLimitsNotConfigured
		}
		return nil, err
	}

	now := c.now()
	usage, err := c.store.KYC().UsageForWindows(ctx, userID, currency, map[string]string{
		models.LimitWindowDaily:   helpers.DayKey(now),
		models.LimitWindowMonthly: helpers.MonthKey(now),
	})
	if err != nil {
		return nil, err
	}
	daily := usage[models.LimitWindowDaily]
	monthly := usage[models.LimitWindowMonthly]

	return &LimitSnapshot{
		Tier:                      profile.Tier,
		Currency:                  currency,
		SingleTransactionMaxMinor: limit.SingleTransactionMaxMinor,
		DailyMaxMinor:             limit.DailyMaxMinor,
		DailyUsedMinor:            daily.UsedMinor,
		DailyRemainingMinor:       remaining(limit.DailyMaxMinor, daily.UsedMinor),
		MonthlyMaxMinor:           limit.MonthlyMaxMinor,
		MonthlyUsedMinor:          monthly.UsedMinor,
		MonthlyRemainingMinor:     remaining(limit.MonthlyMaxMinor, monthly.UsedMinor),
		MaxBalanceMinor:           limit.MaxBalanceMinor,
		DailyCountMax:             limit.DailyCountMax,
		DailyCountUsed:            daily.UsedCount,
	}, nil
}

func (c *limitController) UpsertLimit(ctx context.Context, limit *models.TransactionLimit) error {
	if limit.DailyMaxMinor > 0 && limit.SingleTransactionMaxMinor > limit.DailyMaxMinor {
		return fmt.Errorf("single transaction limit cannot exceed the daily limit")
	}
	if limit.MonthlyMaxMinor > 0 && limit.DailyMaxMinor > limit.MonthlyMaxMinor {
		return fmt.Errorf("daily limit cannot exceed the monthly limit")
	}
	return c.store.KYC().UpsertLimit(ctx, limit)
}

func (c *limitController) ListLimits(ctx context.Context) ([]models.TransactionLimit, error) {
	return c.store.KYC().ListLimits(ctx)
}

func remaining(max, used int64) int64 {
	if max <= 0 {
		return 0
	}
	if used >= max {
		return 0
	}
	return max - used
}

func DefaultTransactionLimits() []models.TransactionLimit {
	return []models.TransactionLimit{
		{
			Tier:                      models.KYCTier0,
			Currency:                  "NGN",
			SingleTransactionMaxMinor: 0,
			DailyMaxMinor:             0,
			MonthlyMaxMinor:           0,
			MaxBalanceMinor:           0,
			DailyCountMax:             0,
		},
		{
			Tier:                      models.KYCTier1,
			Currency:                  "NGN",
			SingleTransactionMaxMinor: 50_000 * messages.NairaMinorUnits,
			DailyMaxMinor:             50_000 * messages.NairaMinorUnits,
			MonthlyMaxMinor:           300_000 * messages.NairaMinorUnits,
			MaxBalanceMinor:           300_000 * messages.NairaMinorUnits,
			DailyCountMax:             20,
		},
		{
			Tier:                      models.KYCTier2,
			Currency:                  "NGN",
			SingleTransactionMaxMinor: 200_000 * messages.NairaMinorUnits,
			DailyMaxMinor:             200_000 * messages.NairaMinorUnits,
			MonthlyMaxMinor:           2_000_000 * messages.NairaMinorUnits,
			MaxBalanceMinor:           500_000 * messages.NairaMinorUnits,
			DailyCountMax:             50,
		},
		{
			Tier:                      models.KYCTier3,
			Currency:                  "NGN",
			SingleTransactionMaxMinor: 1_000_000 * messages.NairaMinorUnits,
			DailyMaxMinor:             5_000_000 * messages.NairaMinorUnits,
			MonthlyMaxMinor:           50_000_000 * messages.NairaMinorUnits,
			MaxBalanceMinor:           0,
			DailyCountMax:             0,
		},
	}
}
