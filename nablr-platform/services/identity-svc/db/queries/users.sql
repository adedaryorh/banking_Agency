-- name: CreateUser :one
INSERT INTO users (phone_number, otp_code, otp_expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByPhone :one
SELECT * FROM users WHERE phone_number = $1;

-- name: SetOTP :one
UPDATE users SET otp_code = $2, otp_expires_at = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: VerifyPhone :one
UPDATE users SET phone_verified = true, phone_verified_at = now(), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetNINStatus :one
UPDATE users SET nin_status = $2, nin_verified = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetFacialStatus :one
UPDATE users SET facial_status = $2, facial_verified = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetPassword :one
UPDATE users SET password_hash = $2, password_set = true, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateLastLogin :exec
UPDATE users SET last_login_at = now() WHERE id = $1;

-- name: UpdateUserAvatar :one
UPDATE users SET avatar_url = $2, updated_at = now()
WHERE id = $1
RETURNING *;
