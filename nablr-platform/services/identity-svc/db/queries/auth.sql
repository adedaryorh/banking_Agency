-- name: CreateAuthUser :one
INSERT INTO users (id,email,phone_number,password_hash,password_set,role,status,email_verified_at,last_login_at)
VALUES ($1,$2,$3,$4,true,$5,$6,$7,$8) RETURNING *;
-- name: AuthUserByID :one
SELECT * FROM users WHERE id=$1;
-- name: AuthUserByEmail :one
SELECT * FROM users WHERE lower(email)=lower($1);
-- name: UpdateAuthUser :one
UPDATE users SET email=$2,phone_number=$3,password_hash=$4,role=$5,status=$6,email_verified_at=$7,last_login_at=$8,updated_at=now() WHERE id=$1 RETURNING *;
-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (id,user_id,token_hash,family_id,user_agent,ip_address,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *;
-- name: RefreshTokenByHash :one
SELECT * FROM refresh_tokens WHERE token_hash=$1;
-- name: RevokeRefreshToken :execrows
UPDATE refresh_tokens SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL;
-- name: RevokeTokenFamily :exec
UPDATE refresh_tokens SET revoked_at=$2 WHERE family_id=$1 AND revoked_at IS NULL;
-- name: RevokeUserRefreshTokens :exec
UPDATE refresh_tokens SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL;
-- name: CreateVerificationToken :one
INSERT INTO verification_tokens (id,user_id,type,token_hash,requested_ip,user_agent,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *;
-- name: VerificationTokenByHash :one
SELECT * FROM verification_tokens WHERE token_hash=$1;
-- name: ConsumeVerificationToken :execrows
UPDATE verification_tokens SET used_at=$2 WHERE id=$1 AND used_at IS NULL AND expires_at>$2;
-- name: InvalidateVerificationTokens :exec
UPDATE verification_tokens SET used_at=$3 WHERE user_id=$1 AND type=$2 AND used_at IS NULL;

-- name: SoftDeleteUser :one
UPDATE users SET status='deactivated', deleted_at=now(), updated_at=now() WHERE id=$1 AND deleted_at IS NULL RETURNING *;

-- name: RestoreUser :one
UPDATE users SET status='pending_verification', deleted_at=NULL, updated_at=now() WHERE id=$1 AND deleted_at IS NOT NULL RETURNING *;

-- name: AuthUserByIDIncludingDeleted :one
SELECT * FROM users WHERE id=$1;

-- name: AuthUserByEmailIncludingDeleted :one
SELECT * FROM users WHERE lower(email)=lower($1);

-- name: AuthUserByPhoneIncludingDeleted :one
SELECT * FROM users WHERE phone_number=$1;

-- name: UserByAccountNumber :one
SELECT * FROM users WHERE account_number=$1 AND deleted_at IS NULL;
