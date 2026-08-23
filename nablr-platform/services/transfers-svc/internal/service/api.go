package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "nabla/transfers-svc/db/sqlc"
	"nabla/transfers-svc/internal/models"
	"nabla/transfers-svc/internal/providers"
)

func notFound(sentinel, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.Join(sentinel, err)
	}
	return err
}

func (s *Service) BeneficiaryByID(ctx context.Context, u, id uuid.UUID) (db.Beneficiary, error) {
	b, err := s.q.BeneficiaryByID(ctx, db.BeneficiaryByIDParams{ID: id, UserID: u})
	if err != nil {
		return db.Beneficiary{}, notFound(models.ErrBeneficiaryNotFound, err)
	}
	return b, nil
}

func (s *Service) UpdateBeneficiary(ctx context.Context, u, id uuid.UUID, nickname *string, favourite, saved *bool) (db.Beneficiary, error) {
	p := db.UpdateBeneficiaryPresentationParams{ID: id, UserID: u}
	if nickname != nil {
		p.Nickname = pgtype.Text{String: *nickname, Valid: true}
	}
	if favourite != nil {
		p.IsFavourite = pgtype.Bool{Bool: *favourite, Valid: true}
	}
	if saved != nil {
		p.IsSaved = pgtype.Bool{Bool: *saved, Valid: true}
	}
	b, err := s.q.UpdateBeneficiaryPresentation(ctx, p)
	if err != nil {
		return db.Beneficiary{}, notFound(models.ErrBeneficiaryNotFound, err)
	}
	return b, nil
}

func (s *Service) RemoveBeneficiary(ctx context.Context, u, id uuid.UUID) error {
	n, err := s.q.DeleteBeneficiary(ctx, db.DeleteBeneficiaryParams{ID: id, UserID: u})
	if err != nil {
		return err
	}
	if n == 0 {
		return models.ErrBeneficiaryNotFound
	}
	return nil
}

func (s *Service) ResolveAccount(ctx context.Context, bankCode, accountNumber string) (*providers.AccountValidation, error) {
	if bankCode == "" || len(accountNumber) < 6 {
		return nil, errors.New("bank code and a 6+ digit account number are required")
	}
	if s.payout == nil {
		return nil, models.ErrBankUnsupported
	}
	providerCode, err := s.providerBankCode(ctx, bankCode)
	if err != nil {
		return nil, models.ErrBankUnsupported
	}
	return s.payout.ValidateAccount(ctx, "NG", providerCode, accountNumber)
}

func (s *Service) QuoteByID(ctx context.Context, u, id uuid.UUID) (db.TransferQuote, error) {
	q, err := s.q.QuoteByID(ctx, db.QuoteByIDParams{ID: id, UserID: u})
	if err != nil {
		return db.TransferQuote{}, notFound(models.ErrQuoteNotFound, err)
	}
	return q, nil
}

func (s *Service) ListQuotes(ctx context.Context, u uuid.UUID, limit int32) ([]db.TransferQuote, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	return s.q.ListQuotes(ctx, db.ListQuotesParams{UserID: u, Limit: limit})
}

type LimitsView struct {
	Currency            string
	PerTransactionMinor int64
	DailyOutboundMinor  int64

	BalanceCapMinor int64
	HasBalanceCap   bool
	DailySpentMinor int64
	RemainingMinor  int64
}

func (s *Service) Limits(ctx context.Context, u uuid.UUID, cur string) (LimitsView, error) {
	c, err := currency(cur)
	if err != nil {
		return LimitsView{}, err
	}
	perTx, hasPerTx, err := activeAmountLimit(ctx, s.q, u, c, limitPerTransaction)
	if err != nil {
		return LimitsView{}, err
	}
	daily, hasDaily, err := activeAmountLimit(ctx, s.q, u, c, limitDailyOutbound)
	if err != nil {
		return LimitsView{}, err
	}
	capMinor, hasCap, err := activeAmountLimit(ctx, s.q, u, c, limitBalanceCap)
	if err != nil {
		return LimitsView{}, err
	}
	// No materialised outbound ceiling yet: derive from the payer's tier, the same
	// source Transfer materialises from. balance_cap is intentionally not part of
	// this gate — its absence is a valid Tier 3 answer, not "not set".
	if !hasPerTx || !hasDaily {
		if s.identity == nil {
			return LimitsView{}, models.ErrAuthorizationUnavailable
		}
		kyc, e := s.identity.GetKYCProfile(ctx, u)
		if e != nil {
			return LimitsView{}, authzError(e)
		}
		ladder, ok := cbnTierLadder[kyc.Tier]
		if !ok {
			return LimitsView{}, models.ErrTierLimitsNotSet
		}
		if !hasPerTx {
			perTx = ladder.perTransactionMinor
		}
		if !hasDaily {
			daily = ladder.dailyOutboundMinor
		}
		if !hasCap && ladder.balanceCapMinor != noBalanceCap {
			capMinor, hasCap = ladder.balanceCapMinor, true
		}
	}
	used, err := s.q.OutboundToday(ctx, db.OutboundTodayParams{SenderUserID: u, SendCurrency: c})
	if err != nil {
		return LimitsView{}, err
	}
	remaining := daily - used
	if remaining < 0 {
		remaining = 0
	}
	return LimitsView{
		Currency:            c,
		PerTransactionMinor: perTx,
		DailyOutboundMinor:  daily,
		BalanceCapMinor:     capMinor,
		HasBalanceCap:       hasCap,
		DailySpentMinor:     used,
		RemainingMinor:      remaining,
	}, nil
}

func (s *Service) SettlePayoutByReference(ctx context.Context, reference string) error {
	if reference == "" {
		return errors.New("provider reference is required")
	}
	return s.HandlePayoutSettled(ctx, s.PayoutRailName(), reference)
}

func (s *Service) FailPayoutByReference(ctx context.Context, reference, reason string) error {
	if reference == "" {
		return errors.New("provider reference is required")
	}
	return s.HandlePayoutFailed(ctx, s.PayoutRailName(), reference, reason)
}

func (s *Service) PayoutRailName() string {
	if s.payout == nil {
		return ""
	}
	return s.payout.Info().Name
}

func (s *Service) AcceptProviderWebhook(ctx context.Context, eventID, eventType, digest string, payload []byte, signatureValid bool) (bool, error) {
	if eventID == "" {
		return false, errors.New("event id is required")
	}
	return s.RecordProviderWebhook(ctx, s.PayoutRailName(), eventID, eventType, digest, payload, signatureValid)
}

func (s *Service) PayoutStatusByTransferID(ctx context.Context, u, id uuid.UUID) (string, error) {
	t, err := s.TransferByID(ctx, u, id)
	if err != nil {
		return "", err
	}
	if !t.ProviderReference.Valid || t.ProviderReference.String == "" {
		return "not_dispatched", nil
	}
	r, err := s.payout.PayoutStatus(ctx, t.ProviderReference.String)
	if err != nil {
		return "", err
	}
	return string(r.Status), nil
}

// VerifyPayoutOutcome asks the payout rail for its current state. Webhook
// payloads are notifications, not authority for moving customer money.
func (s *Service) VerifyPayoutOutcome(ctx context.Context, reference string) (PayoutOutcome, bool, string, error) {
	if s.payout == nil {
		return 0, false, "", errors.New("payout provider is not configured")
	}
	result, err := s.payout.PayoutStatus(ctx, reference)
	if err != nil {
		return 0, false, "", err
	}
	verifiedRef := result.ProviderRef
	if verifiedRef == "" {
		verifiedRef = reference
	}
	switch result.Status {
	case providers.PayoutSettled:
		return PayoutSettled, true, verifiedRef, nil
	case providers.PayoutFailed, providers.PayoutReturned:
		return PayoutFailed, true, verifiedRef, nil
	default:
		return 0, false, verifiedRef, nil
	}
}

func checkNewPayeeAllowance(b db.Beneficiary, amountMinor int64, cur string, now time.Time) error {
	if !b.CoolingPeriodEndsAt.Valid || !now.Before(b.CoolingPeriodEndsAt.Time) {
		return nil
	}
	if amountMinor > models.NewPayeeAllowance(cur) {
		return models.ErrBeneficiaryCooling
	}
	return nil
}

type WalletView struct {
	ID             uuid.UUID
	Currency       string
	Status         string
	AvailableMinor int64
	ReservedMinor  int64
	UpdatedAt      time.Time
}

func (s *Service) WalletView(ctx context.Context, u uuid.UUID, cur string) (WalletView, error) {
	w, err := s.Wallet(ctx, u, cur)
	if err != nil {
		return WalletView{}, notFound(models.ErrWalletNotFound, err)
	}
	return WalletView{
		ID: w.ID, Currency: w.Currency, Status: w.Status,
		AvailableMinor: w.AvailableMinor, ReservedMinor: w.ReservedMinor,
		UpdatedAt: w.UpdatedAt,
	}, nil
}
