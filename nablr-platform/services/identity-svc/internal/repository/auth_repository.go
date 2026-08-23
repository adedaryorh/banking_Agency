package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "nabla/identity-svc/db/sqlc"
	"nabla/identity-svc/internal/models"
)

type AuthRepository interface {
	CreateUser(context.Context, *models.User) error
	CreatePhoneUser(context.Context, string) (*models.User, error)
	UserByID(context.Context, uuid.UUID) (*models.User, error)
	UserByEmail(context.Context, string) (*models.User, error)
	UserByPhone(context.Context, string) (*models.User, error)
	MarkPhoneVerified(context.Context, uuid.UUID) (*models.User, error)
	SetPassword(context.Context, uuid.UUID, string) (*models.User, error)
	UpdateUser(context.Context, *models.User) error
	ClaimWaitlistUsername(context.Context, uuid.UUID, string, time.Time) (*models.User, error)
	CreateRefreshToken(context.Context, *models.RefreshToken) error
	RefreshTokenByHash(context.Context, string) (*models.RefreshToken, error)
	RevokeRefreshToken(context.Context, uuid.UUID, time.Time) error
	RevokeTokenFamily(context.Context, uuid.UUID, time.Time) error
	RevokeUserRefreshTokens(context.Context, uuid.UUID, time.Time) error
	CreateVerificationToken(context.Context, *models.VerificationToken) error
	VerificationTokenByHash(context.Context, string) (*models.VerificationToken, error)
	ConsumeVerificationToken(context.Context, uuid.UUID, time.Time) error
	InvalidateVerificationTokens(context.Context, uuid.UUID, models.VerificationTokenType, time.Time) error
	// Soft delete / account deactivation
	SoftDeleteUser(context.Context, uuid.UUID) (*models.User, error)
	RestoreUser(context.Context, uuid.UUID) (*models.User, error)
	UserByIDIncludingDeleted(context.Context, uuid.UUID) (*models.User, error)
	UserByEmailIncludingDeleted(context.Context, string) (*models.User, error)
	UserByPhoneIncludingDeleted(context.Context, string) (*models.User, error)
	// Account closure requests. This table isn't sqlc-managed (see the
	// hand-written queries at the bottom of this file), so these go straight
	// through the pool rather than through r.queries.
	CreateAccountClosureRequest(context.Context, *models.AccountClosureRequest) error
	AccountClosureRequestByID(context.Context, uuid.UUID) (*models.AccountClosureRequest, error)
	LatestAccountClosureRequest(context.Context, uuid.UUID) (*models.AccountClosureRequest, error)
	UpdateAccountClosureRequest(context.Context, *models.AccountClosureRequest) error
}

func (r *authRepository) ClaimWaitlistUsername(ctx context.Context, userID uuid.UUID, tokenHash string, now time.Time) (*models.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var entryID uuid.UUID
	var username, status string
	var expiresAt *time.Time
	var claimedUserID *uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id, nablr_username, status, expires_at, claimed_user_id
		FROM waitlist_entries
		WHERE reservation_token_hash=$1
		FOR UPDATE`, tokenHash).Scan(&entryID, &username, &status, &expiresAt, &claimedUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status == models.WaitlistStatusOnboarded && claimedUserID != nil && *claimedUserID == userID {
		row, queryErr := identitydb.New(tx).AuthUserByID(ctx, userID)
		if queryErr != nil {
			return nil, mapDBError(queryErr)
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		value := mapped[models.User](row)
		return &value, nil
	}
	if status != models.WaitlistStatusConfirmed || expiresAt == nil || !expiresAt.After(now) {
		return nil, ErrExpired
	}

	row := tx.QueryRow(ctx, `
		UPDATE users
		SET nablr_username=$2, updated_at=$3
		WHERE id=$1 AND deleted_at IS NULL
		  AND (nablr_username IS NULL OR lower(nablr_username)=lower($2))
		RETURNING id,email,phone_number,nablr_username,password_hash,password_set,role,status,
		          phone_verified,phone_verified_at,otp_code,otp_expires_at,nin_status,nin_verified,
		          facial_status,facial_verified,email_verified_at,last_login_at,deleted_at,created_at,
		          updated_at,account_number,avatar_url`, userID, username, now)
	var dbUser identitydb.User
	if err = row.Scan(&dbUser.ID, &dbUser.Email, &dbUser.PhoneNumber, &dbUser.NablrUsername,
		&dbUser.PasswordHash, &dbUser.PasswordSet, &dbUser.Role, &dbUser.Status,
		&dbUser.PhoneVerified, &dbUser.PhoneVerifiedAt, &dbUser.OtpCode, &dbUser.OtpExpiresAt,
		&dbUser.NinStatus, &dbUser.NinVerified, &dbUser.FacialStatus, &dbUser.FacialVerified,
		&dbUser.EmailVerifiedAt, &dbUser.LastLoginAt, &dbUser.DeletedAt, &dbUser.CreatedAt,
		&dbUser.UpdatedAt, &dbUser.AccountNumber, &dbUser.AvatarUrl); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConflict
	} else if err != nil {
		return nil, mapDBError(err)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE waitlist_entries
		SET status='onboarded', claimed_user_id=$2, claimed_at=$3, updated_at=$3
		WHERE id=$1`, entryID, userID, now); err != nil {
		return nil, mapDBError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, mapDBError(err)
	}
	value := mapped[models.User](dbUser)
	return &value, nil
}

type authRepository struct {
	queries *identitydb.Queries
	pool    *pgxpool.Pool
}

func NewAuthRepository(pool *pgxpool.Pool) AuthRepository {
	return &authRepository{queries: identitydb.New(pool), pool: pool}
}

func mapDBError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}
func ensureID(id *uuid.UUID) {
	if *id == uuid.Nil {
		*id = uuid.New()
	}
}
func (r *authRepository) CreateUser(ctx context.Context, value *models.User) error {
	ensureID(&value.ID)
	row, err := r.queries.CreateAuthUser(ctx, mapped[identitydb.CreateAuthUserParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (r *authRepository) CreatePhoneUser(ctx context.Context, phone string) (*models.User, error) {
	row, err := r.queries.CreateUser(ctx, identitydb.CreateUserParams{PhoneNumber: pgtype.Text{String: phone, Valid: true}})
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (r *authRepository) UserByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	row, err := r.queries.AuthUserByID(ctx, id)
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (r *authRepository) UserByEmail(ctx context.Context, email string) (*models.User, error) {
	row, err := r.queries.AuthUserByEmail(ctx, email)
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (r *authRepository) UserByPhone(ctx context.Context, phone string) (*models.User, error) {
	row, err := r.queries.GetUserByPhone(ctx, pgtype.Text{String: phone, Valid: true})
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (r *authRepository) MarkPhoneVerified(ctx context.Context, id uuid.UUID) (*models.User, error) {
	row, err := r.queries.VerifyPhone(ctx, id)
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (r *authRepository) SetPassword(ctx context.Context, id uuid.UUID, hash string) (*models.User, error) {
	row, err := r.queries.SetPassword(ctx, identitydb.SetPasswordParams{ID: id, PasswordHash: pgtype.Text{String: hash, Valid: true}})
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}
func (r *authRepository) UpdateUser(ctx context.Context, value *models.User) error {
	row, err := r.queries.UpdateAuthUser(ctx, mapped[identitydb.UpdateAuthUserParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (r *authRepository) CreateRefreshToken(ctx context.Context, value *models.RefreshToken) error {
	ensureID(&value.ID)
	row, err := r.queries.CreateRefreshToken(ctx, mapped[identitydb.CreateRefreshTokenParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (r *authRepository) RefreshTokenByHash(ctx context.Context, hash string) (*models.RefreshToken, error) {
	row, err := r.queries.RefreshTokenByHash(ctx, hash)
	value := mapped[models.RefreshToken](row)
	return &value, mapDBError(err)
}
func (r *authRepository) RevokeRefreshToken(ctx context.Context, id uuid.UUID, at time.Time) error {
	count, err := r.queries.RevokeRefreshToken(ctx, identitydb.RevokeRefreshTokenParams{ID: id, RevokedAt: &at})
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return mapDBError(err)
}
func (r *authRepository) RevokeTokenFamily(ctx context.Context, id uuid.UUID, at time.Time) error {
	return mapDBError(r.queries.RevokeTokenFamily(ctx, identitydb.RevokeTokenFamilyParams{FamilyID: id, RevokedAt: &at}))
}
func (r *authRepository) RevokeUserRefreshTokens(ctx context.Context, id uuid.UUID, at time.Time) error {
	return mapDBError(r.queries.RevokeUserRefreshTokens(ctx, identitydb.RevokeUserRefreshTokensParams{UserID: id, RevokedAt: &at}))
}
func (r *authRepository) CreateVerificationToken(ctx context.Context, value *models.VerificationToken) error {
	ensureID(&value.ID)
	row, err := r.queries.CreateVerificationToken(ctx, mapped[identitydb.CreateVerificationTokenParams](value))
	if err == nil {
		copyFields(value, row)
	}
	return mapDBError(err)
}
func (r *authRepository) VerificationTokenByHash(ctx context.Context, hash string) (*models.VerificationToken, error) {
	row, err := r.queries.VerificationTokenByHash(ctx, hash)
	value := mapped[models.VerificationToken](row)
	return &value, mapDBError(err)
}
func (r *authRepository) ConsumeVerificationToken(ctx context.Context, id uuid.UUID, at time.Time) error {
	count, err := r.queries.ConsumeVerificationToken(ctx, identitydb.ConsumeVerificationTokenParams{ID: id, UsedAt: &at})
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return mapDBError(err)
}
func (r *authRepository) InvalidateVerificationTokens(ctx context.Context, id uuid.UUID, kind models.VerificationTokenType, at time.Time) error {
	return mapDBError(r.queries.InvalidateVerificationTokens(ctx, identitydb.InvalidateVerificationTokensParams{UserID: id, Type: string(kind), UsedAt: &at}))
}

func (r *authRepository) SoftDeleteUser(ctx context.Context, id uuid.UUID) (*models.User, error) {
	row, err := r.queries.SoftDeleteUser(ctx, id)
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}

func (r *authRepository) RestoreUser(ctx context.Context, id uuid.UUID) (*models.User, error) {
	row, err := r.queries.RestoreUser(ctx, id)
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}

func (r *authRepository) UserByIDIncludingDeleted(ctx context.Context, id uuid.UUID) (*models.User, error) {
	row, err := r.queries.AuthUserByIDIncludingDeleted(ctx, id)
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}

func (r *authRepository) UserByEmailIncludingDeleted(ctx context.Context, email string) (*models.User, error) {
	row, err := r.queries.AuthUserByEmailIncludingDeleted(ctx, email)
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}

func (r *authRepository) UserByPhoneIncludingDeleted(ctx context.Context, phone string) (*models.User, error) {
	row, err := r.queries.AuthUserByPhoneIncludingDeleted(ctx, pgtype.Text{String: phone, Valid: true})
	value := mapped[models.User](row)
	return &value, mapDBError(err)
}

// --- account_closure_requests -----------------------------------------
//
// This table was added after the sqlc schema was last generated in this
// environment (no working sqlc/protoc toolchain here), so these queries are
// written by hand against the pool directly instead of going through
// db/sqlc. They follow the same column set as the 003 migration and
// db/schema.sql; keep those three in sync if this table changes.

const accountClosureRequestColumns = `id, user_id, reason, reason_note, status, facial_verified_at,
	code_hash, code_expires_at, code_attempts, confirmed_at, restored_at, created_at, updated_at`

func scanAccountClosureRequest(row pgx.Row) (*models.AccountClosureRequest, error) {
	var (
		req            models.AccountClosureRequest
		reason, status string
		reasonNote     *string
		codeHash       *string
	)
	err := row.Scan(
		&req.ID, &req.UserID, &reason, &reasonNote, &status, &req.FacialVerifiedAt,
		&codeHash, &req.CodeExpiresAt, &req.CodeAttempts, &req.ConfirmedAt, &req.RestoredAt,
		&req.CreatedAt, &req.UpdatedAt,
	)
	if err != nil {
		return nil, mapDBError(err)
	}
	req.Reason = models.AccountClosureReason(reason)
	req.Status = models.AccountClosureStatus(status)
	if reasonNote != nil {
		req.ReasonNote = *reasonNote
	}
	if codeHash != nil {
		req.CodeHash = *codeHash
	}
	return &req, nil
}

func (r *authRepository) CreateAccountClosureRequest(ctx context.Context, value *models.AccountClosureRequest) error {
	ensureID(&value.ID)
	var reasonNote *string
	if value.ReasonNote != "" {
		reasonNote = &value.ReasonNote
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO account_closure_requests (id, user_id, reason, reason_note, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+accountClosureRequestColumns,
		value.ID, value.UserID, string(value.Reason), reasonNote, string(value.Status),
	)
	saved, err := scanAccountClosureRequest(row)
	if err != nil {
		return err
	}
	*value = *saved
	return nil
}

func (r *authRepository) AccountClosureRequestByID(ctx context.Context, id uuid.UUID) (*models.AccountClosureRequest, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+accountClosureRequestColumns+` FROM account_closure_requests WHERE id = $1`, id)
	return scanAccountClosureRequest(row)
}

// LatestAccountClosureRequest returns the most recently created closure
// request for a user, regardless of status. Callers decide what to do with
// a request that's already confirmed or cancelled.
func (r *authRepository) LatestAccountClosureRequest(ctx context.Context, userID uuid.UUID) (*models.AccountClosureRequest, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+accountClosureRequestColumns+`
		FROM account_closure_requests
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT 1`, userID,
	)
	return scanAccountClosureRequest(row)
}

func (r *authRepository) UpdateAccountClosureRequest(ctx context.Context, value *models.AccountClosureRequest) error {
	var (
		reasonNote *string
		codeHash   *string
	)
	if value.ReasonNote != "" {
		reasonNote = &value.ReasonNote
	}
	if value.CodeHash != "" {
		codeHash = &value.CodeHash
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE account_closure_requests
		SET reason = $2, reason_note = $3, status = $4, facial_verified_at = $5,
		    code_hash = $6, code_expires_at = $7, code_attempts = $8,
		    confirmed_at = $9, restored_at = $10, updated_at = now()
		WHERE id = $1
		RETURNING `+accountClosureRequestColumns,
		value.ID, string(value.Reason), reasonNote, string(value.Status), value.FacialVerifiedAt,
		codeHash, value.CodeExpiresAt, value.CodeAttempts, value.ConfirmedAt, value.RestoredAt,
	)
	saved, err := scanAccountClosureRequest(row)
	if err != nil {
		return err
	}
	*value = *saved
	return nil
}
