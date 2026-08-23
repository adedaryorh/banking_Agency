package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "nabla/identity-svc/db/sqlc"
	"nabla/identity-svc/internal/models"
)

type sqlcStore struct {
	db      identitydb.DBTX
	queries *identitydb.Queries
}

func NewStore(pool *pgxpool.Pool) Store { return &sqlcStore{db: pool, queries: identitydb.New(pool)} }
func (s *sqlcStore) Atomic(ctx context.Context, operation func(Store) error) error {
	beginner, ok := s.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return errors.New("database does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	child := &sqlcStore{db: tx, queries: s.queries.WithTx(tx)}
	if err = operation(child); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *sqlcStore) Auth() AuthStore         { return s }
func (s *sqlcStore) KYC() KYCStore           { return s }
func (s *sqlcStore) Security() SecurityStore { return s }
func (s *sqlcStore) Tier3() Tier3Store       { return s }
func (s *sqlcStore) Outbox() OutboxStore     { return s }
func (s *sqlcStore) Audit() AuditStore       { return s }
func (s *sqlcStore) Waitlist() WaitlistStore { return s }

func (s *sqlcStore) UserByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	row, err := s.queries.AuthUserByID(ctx, id)
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) UserByPhone(ctx context.Context, phone string) (*models.User, error) {
	row, err := s.queries.GetUserByPhone(ctx, pgtype.Text{String: phone, Valid: true})
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) UserByUsername(ctx context.Context, username string) (*models.User, error) {
	row, err := s.queries.UserByUsername(ctx, username)
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) UserByAccountNumber(ctx context.Context, accountNumber string) (*models.User, error) {
	row, err := s.queries.UserByAccountNumber(ctx, pgtype.Text{String: accountNumber, Valid: true})
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) UpdateUserAvatar(ctx context.Context, id uuid.UUID, avatarURL string) (*models.User, error) {
	row, err := s.queries.UpdateUserAvatar(ctx, identitydb.UpdateUserAvatarParams{
		ID:        id,
		AvatarUrl: pgtype.Text{String: avatarURL, Valid: avatarURL != ""},
	})
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) ProfileByUserID(ctx context.Context, id uuid.UUID) (*models.KYCProfile, error) {
	row, err := s.queries.KYCProfileByUserID(ctx, id)
	value := mapped[models.KYCProfile](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) ProfileByBVNHash(ctx context.Context, hash string) (*models.KYCProfile, error) {
	row, err := s.queries.KYCProfileByBVNHash(ctx, pgtype.Text{String: hash, Valid: true})
	value := mapped[models.KYCProfile](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) ProfileByNINHash(ctx context.Context, hash string) (*models.KYCProfile, error) {
	row, err := s.queries.KYCProfileByNINHash(ctx, pgtype.Text{String: hash, Valid: true})
	value := mapped[models.KYCProfile](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) CreateProfile(ctx context.Context, value *models.KYCProfile) error {
	ensureID(&value.ID)
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC()
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = value.CreatedAt
	}
	row, err := s.queries.CreateKYCProfile(ctx, mapped[identitydb.CreateKYCProfileParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (s *sqlcStore) UpdateProfile(ctx context.Context, value *models.KYCProfile) error {
	row, err := s.queries.UpdateKYCProfile(ctx, mapped[identitydb.UpdateKYCProfileParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (s *sqlcStore) CreateAttempt(ctx context.Context, value *models.KYCVerificationAttempt) error {
	ensureID(&value.ID)
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC()
	}
	_, err := s.queries.CreateKYCAttempt(ctx, mapped[identitydb.CreateKYCAttemptParams](value))
	return mapDBError(err)
}
func (s *sqlcStore) AttemptsByUserID(ctx context.Context, id uuid.UUID, limit int) ([]models.KYCVerificationAttempt, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.queries.KYCAttemptsByUserID(ctx, identitydb.KYCAttemptsByUserIDParams{UserID: id, Limit: int32(limit)})
	values := make([]models.KYCVerificationAttempt, len(rows))
	for i := range rows {
		values[i] = mapped[models.KYCVerificationAttempt](rows[i])
	}
	return values, mapDBError(err)
}
func (s *sqlcStore) CreateFacialCapture(ctx context.Context, value *models.KYCFacialCapture) error {
	ensureID(&value.ID)
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC()
	}
	_, err := s.queries.CreateKYCFacialCapture(ctx, mapped[identitydb.CreateKYCFacialCaptureParams](value))
	return mapDBError(err)
}
func (s *sqlcStore) RecentAttemptCount(ctx context.Context, id uuid.UUID, kind string, since time.Time) (int64, error) {
	return s.queries.RecentKYCAttemptCount(ctx, identitydb.RecentKYCAttemptCountParams{UserID: id, Kind: kind, CreatedAt: since})
}
func (s *sqlcStore) LimitForTier(ctx context.Context, tier models.KYCTier, currency string) (*models.TransactionLimit, error) {
	row, err := s.queries.LimitForTier(ctx, identitydb.LimitForTierParams{Tier: int16(tier), Currency: currency})
	value := mapped[models.TransactionLimit](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) LockUsage(ctx context.Context, userID uuid.UUID, currency, windowType, windowKey string) (*models.LimitUsage, error) {
	arg := identitydb.EnsureLimitUsageParams{UserID: userID, Currency: currency, WindowType: windowType, WindowKey: windowKey}
	if err := s.queries.EnsureLimitUsage(ctx, arg); err != nil {
		return nil, mapDBError(err)
	}
	row, err := s.queries.LockLimitUsage(ctx, identitydb.LockLimitUsageParams(arg))
	value := mapped[models.LimitUsage](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) ApplyUsageDelta(ctx context.Context, id uuid.UUID, amount int64, count int) error {
	rows, err := s.queries.ApplyLimitUsageDelta(ctx, identitydb.ApplyLimitUsageDeltaParams{ID: id, UsedMinor: amount, UsedCount: int32(count)})
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return mapDBError(err)
}
func (s *sqlcStore) UsageForWindows(ctx context.Context, userID uuid.UUID, currency string, windows map[string]string) (map[string]models.LimitUsage, error) {
	values := make(map[string]models.LimitUsage, len(windows))
	for kind, key := range windows {
		row, err := s.queries.UsageForWindow(ctx, identitydb.UsageForWindowParams{UserID: userID, Currency: currency, WindowType: kind, WindowKey: key})
		if errors.Is(err, pgx.ErrNoRows) {
			values[kind] = models.LimitUsage{UserID: userID, Currency: currency, WindowType: kind, WindowKey: key}
			continue
		}
		if err != nil {
			return nil, mapDBError(err)
		}
		values[kind] = mapped[models.LimitUsage](row)
	}
	return values, nil
}
func (s *sqlcStore) UpsertLimit(ctx context.Context, value *models.TransactionLimit) error {
	ensureID(&value.ID)
	if value.CreatedAt.IsZero() {
		value.CreatedAt = time.Now().UTC()
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = value.CreatedAt
	}
	row, err := s.queries.UpsertTransactionLimit(ctx, mapped[identitydb.UpsertTransactionLimitParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (s *sqlcStore) ListLimits(ctx context.Context) ([]models.TransactionLimit, error) {
	rows, err := s.queries.ListTransactionLimits(ctx)
	values := make([]models.TransactionLimit, len(rows))
	for i := range rows {
		values[i] = mapped[models.TransactionLimit](rows[i])
	}
	return values, mapDBError(err)
}

func (s *sqlcStore) PINByUserID(ctx context.Context, id uuid.UUID) (*models.TransactionPIN, error) {
	row, err := s.queries.PINByUserID(ctx, id)
	value := mapped[models.TransactionPIN](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) LockPINByUserID(ctx context.Context, id uuid.UUID) (*models.TransactionPIN, error) {
	row, err := s.queries.LockPINByUserID(ctx, id)
	value := mapped[models.TransactionPIN](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) CreatePIN(ctx context.Context, value *models.TransactionPIN) error {
	ensureID(&value.ID)
	_, err := s.queries.CreatePIN(ctx, mapped[identitydb.CreatePINParams](value))
	return mapDBError(err)
}
func (s *sqlcStore) UpdatePINHash(ctx context.Context, id uuid.UUID, hash string, at time.Time) error {
	rows, err := s.queries.UpdatePINHash(ctx, identitydb.UpdatePINHashParams{UserID: id, PinHash: hash, LastChangedAt: at})
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return mapDBError(err)
}
func (s *sqlcStore) RecordPINFailure(ctx context.Context, id uuid.UUID, locked *time.Time) error {
	return mapDBError(s.queries.RecordPINFailure(ctx, identitydb.RecordPINFailureParams{ID: id, LockedUntil: locked}))
}
func (s *sqlcStore) ResetPINAttempts(ctx context.Context, id uuid.UUID, at time.Time) error {
	return mapDBError(s.queries.ResetPINAttempts(ctx, identitydb.ResetPINAttemptsParams{ID: id, LastUsedAt: &at}))
}
func (s *sqlcStore) RecentOTPCount(ctx context.Context, id uuid.UUID, purpose models.OTPPurpose, since time.Time) (int64, error) {
	return s.queries.RecentOTPCount(ctx, identitydb.RecentOTPCountParams{UserID: id, Purpose: string(purpose), CreatedAt: since})
}
func (s *sqlcStore) InvalidateOTPs(ctx context.Context, id uuid.UUID, purpose models.OTPPurpose, at time.Time) error {
	return mapDBError(s.queries.InvalidateOTPs(ctx, identitydb.InvalidateOTPsParams{UserID: id, Purpose: string(purpose), ConsumedAt: &at}))
}
func (s *sqlcStore) CreateOTP(ctx context.Context, value *models.OTPCode) error {
	ensureID(&value.ID)
	_, err := s.queries.CreateOTP(ctx, mapped[identitydb.CreateOTPParams](value))
	return mapDBError(err)
}
func (s *sqlcStore) OTPByChallengeToken(ctx context.Context, token string) (*models.OTPCode, error) {
	row, err := s.queries.OTPByChallengeToken(ctx, token)
	value := mapped[models.OTPCode](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) RecordOTPFailure(ctx context.Context, id uuid.UUID) error {
	return mapDBError(s.queries.RecordOTPFailure(ctx, id))
}
func (s *sqlcStore) ConsumeOTP(ctx context.Context, id uuid.UUID, at time.Time) error {
	rows, err := s.queries.ConsumeOTP(ctx, identitydb.ConsumeOTPParams{ID: id, ConsumedAt: &at})
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return mapDBError(err)
}
func (s *sqlcStore) DeviceByFingerprint(ctx context.Context, userID uuid.UUID, fingerprint string) (*models.UserDevice, error) {
	row, err := s.queries.DeviceByFingerprint(ctx, identitydb.DeviceByFingerprintParams{UserID: userID, Fingerprint: fingerprint})
	value := mapped[models.UserDevice](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) DeviceByID(ctx context.Context, id uuid.UUID) (*models.UserDevice, error) {
	row, err := s.queries.DeviceByID(ctx, id)
	value := mapped[models.UserDevice](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) TouchDevice(ctx context.Context, id uuid.UUID, ip string, at time.Time) error {
	return mapDBError(s.queries.TouchDevice(ctx, identitydb.TouchDeviceParams{ID: id, LastIp: pgtype.Text{String: ip, Valid: ip != ""}, LastSeenAt: &at}))
}
func (s *sqlcStore) UpsertDevice(ctx context.Context, value *models.UserDevice) error {
	ensureID(&value.ID)
	row, err := s.queries.UpsertDevice(ctx, mapped[identitydb.UpsertDeviceParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (s *sqlcStore) DevicesByUserID(ctx context.Context, id uuid.UUID) ([]models.UserDevice, error) {
	rows, err := s.queries.DevicesByUserID(ctx, id)
	values := make([]models.UserDevice, len(rows))
	for i := range rows {
		values[i] = mapped[models.UserDevice](rows[i])
	}
	return values, mapDBError(err)
}
func (s *sqlcStore) SetDeviceTrusted(ctx context.Context, id uuid.UUID, trusted bool, at time.Time) error {
	return mapDBError(s.queries.SetDeviceTrusted(ctx, identitydb.SetDeviceTrustedParams{ID: id, Trusted: trusted, TrustedAt: &at}))
}
func (s *sqlcStore) SetDeviceBlocked(ctx context.Context, id uuid.UUID, blocked bool) error {
	return mapDBError(s.queries.SetDeviceBlocked(ctx, identitydb.SetDeviceBlockedParams{ID: id, Blocked: blocked}))
}
func (s *sqlcStore) RecordAssessment(ctx context.Context, value *models.RiskAssessment) error {
	ensureID(&value.ID)
	_, err := s.queries.RecordRiskAssessment(ctx, mapped[identitydb.RecordRiskAssessmentParams](value))
	return mapDBError(err)
}
func (s *sqlcStore) AssessmentsByUserID(ctx context.Context, id uuid.UUID, limit int) ([]models.RiskAssessment, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.queries.AssessmentsByUserID(ctx, identitydb.AssessmentsByUserIDParams{UserID: id, Limit: int32(limit)})
	values := make([]models.RiskAssessment, len(rows))
	for i := range rows {
		values[i] = mapped[models.RiskAssessment](rows[i])
	}
	return values, mapDBError(err)
}

func (s *sqlcStore) ApplicationByUserID(ctx context.Context, id uuid.UUID) (*models.Tier3Application, error) {
	row, err := s.queries.Tier3ApplicationByUserID(ctx, id)
	value := mapped[models.Tier3Application](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) CreateApplication(ctx context.Context, value *models.Tier3Application) error {
	ensureID(&value.ID)
	row, err := s.queries.CreateTier3Application(ctx, mapped[identitydb.CreateTier3ApplicationParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (s *sqlcStore) UpdateApplication(ctx context.Context, value *models.Tier3Application) error {
	row, err := s.queries.UpdateTier3Application(ctx, mapped[identitydb.UpdateTier3ApplicationParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (s *sqlcStore) CreateDocument(ctx context.Context, value *models.Tier3Document) error {
	ensureID(&value.ID)
	row, err := s.queries.CreateTier3Document(ctx, mapped[identitydb.CreateTier3DocumentParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (s *sqlcStore) DocumentByID(ctx context.Context, userID, id uuid.UUID) (*models.Tier3Document, error) {
	row, err := s.queries.Tier3DocumentByID(ctx, identitydb.Tier3DocumentByIDParams{ID: id, UserID: userID})
	value := mapped[models.Tier3Document](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) Enqueue(ctx context.Context, value *models.OutboxEvent) error {
	ensureID(&value.ID)
	row, err := s.queries.EnqueueOutbox(ctx, mapped[identitydb.EnqueueOutboxParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (s *sqlcStore) PendingCount(ctx context.Context) (int64, error) {
	return s.queries.PendingOutboxCount(ctx)
}
func (s *sqlcStore) ClaimBatch(ctx context.Context, limit int, now time.Time) ([]models.OutboxEvent, error) {
	rows, err := s.queries.ClaimOutboxBatch(ctx, identitydb.ClaimOutboxBatchParams{AvailableAt: now, Limit: int32(limit)})
	values := make([]models.OutboxEvent, len(rows))
	for i := range rows {
		values[i] = mapped[models.OutboxEvent](rows[i])
	}
	return values, mapDBError(err)
}
func (s *sqlcStore) MarkPublished(ctx context.Context, id uuid.UUID, at time.Time) error {
	return mapDBError(s.queries.MarkOutboxPublished(ctx, identitydb.MarkOutboxPublishedParams{ID: id, PublishedAt: &at}))
}
func (s *sqlcStore) MarkFailed(ctx context.Context, id uuid.UUID, message string, next time.Time) error {
	return mapDBError(s.queries.MarkOutboxFailed(ctx, identitydb.MarkOutboxFailedParams{ID: id, LastError: pgtype.Text{String: message, Valid: true}, AvailableAt: next}))
}
func (s *sqlcStore) MarkDeadLetter(ctx context.Context, id uuid.UUID, message string) error {
	return mapDBError(s.queries.MarkOutboxDeadLetter(ctx, identitydb.MarkOutboxDeadLetterParams{ID: id, LastError: pgtype.Text{String: message, Valid: true}}))
}
func (s *sqlcStore) Replay(ctx context.Context, id uuid.UUID) error {
	return mapDBError(s.queries.ReplayOutboxEvent(ctx, id))
}
func (s *sqlcStore) DeletePublishedBefore(ctx context.Context, before time.Time) (int64, error) {
	return s.queries.DeletePublishedOutboxBefore(ctx, &before)
}
func (s *sqlcStore) Record(ctx context.Context, value *models.AuditLog) error {
	ensureID(&value.ID)
	_, err := s.queries.RecordAuditLog(ctx, mapped[identitydb.RecordAuditLogParams](value))
	return mapDBError(err)
}
func (s *sqlcStore) Search(ctx context.Context, filter AuditFilter) ([]models.AuditLog, int64, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	base := identitydb.SearchAuditLogsParams{Limit: int32(limit), Offset: int32(filter.Offset), ActorID: filter.ActorID, EntityType: pgtype.Text{String: filter.EntityType, Valid: filter.EntityType != ""}, EntityID: filter.EntityID, Action: pgtype.Text{String: filter.Action, Valid: filter.Action != ""}, FromTime: filter.From, ToTime: filter.To}
	rows, err := s.queries.SearchAuditLogs(ctx, base)
	if err != nil {
		return nil, 0, mapDBError(err)
	}
	countArg := mapped[identitydb.CountAuditLogsParams](base)
	total, err := s.queries.CountAuditLogs(ctx, countArg)
	values := make([]models.AuditLog, len(rows))
	for i := range rows {
		values[i] = mapped[models.AuditLog](rows[i])
	}
	return values, total, mapDBError(err)
}

func (s *sqlcStore) UsernameExists(ctx context.Context, username string) (bool, error) {
	_, err := s.queries.UserByUsername(ctx, username)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, err
}
func (s *sqlcStore) ExpireStaleByUsername(ctx context.Context, username string, now time.Time) error {
	return mapDBError(s.queries.ExpireStaleWaitlistByUsername(ctx, identitydb.ExpireStaleWaitlistByUsernameParams{Lower: username, HoldExpiresAt: now}))
}
func (s *sqlcStore) ActiveByUsername(ctx context.Context, username string) (*models.WaitlistEntry, error) {
	row, err := s.queries.ActiveWaitlistEntryByUsername(ctx, username)
	value := mapped[models.WaitlistEntry](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) CreateWaitlistEntry(ctx context.Context, value *models.WaitlistEntry) error {
	ensureID(&value.ID)
	row, err := s.queries.CreateWaitlistEntry(ctx, mapped[identitydb.CreateWaitlistEntryParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (s *sqlcStore) ByReservationTokenHash(ctx context.Context, hash string) (*models.WaitlistEntry, error) {
	row, err := s.queries.WaitlistEntryByReservationTokenHash(ctx, hash)
	value := mapped[models.WaitlistEntry](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) ExpireWaitlistEntry(ctx context.Context, id uuid.UUID, at time.Time) error {
	return mapDBError(s.queries.ExpireWaitlistEntry(ctx, identitydb.ExpireWaitlistEntryParams{ID: id, UpdatedAt: at}))
}
func (s *sqlcStore) ConfirmWaitlistEntry(ctx context.Context, id uuid.UUID, email string, at, expiresAt time.Time) (*models.WaitlistEntry, error) {
	row, err := s.queries.ConfirmWaitlistEntry(ctx, identitydb.ConfirmWaitlistEntryParams{
		ID:          id,
		Email:       pgtype.Text{String: email, Valid: email != ""},
		ConfirmedAt: &at,
		ExpiresAt:   &expiresAt,
	})
	value := mapped[models.WaitlistEntry](row)
	return &value, mapDBError(err)
}
func (s *sqlcStore) ConfirmedWaitlistEntryByEmail(ctx context.Context, email string) (*models.WaitlistEntry, error) {
	row, err := s.queries.ConfirmedWaitlistEntryByEmail(ctx, email)
	value := mapped[models.WaitlistEntry](row)
	return &value, mapDBError(err)
}

var _ Store = (*sqlcStore)(nil)
