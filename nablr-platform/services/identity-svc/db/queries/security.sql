-- name: PINByUserID :one
SELECT * FROM transaction_pins WHERE user_id=$1;
-- name: LockPINByUserID :one
SELECT * FROM transaction_pins WHERE user_id=$1 FOR UPDATE;
-- name: CreatePIN :one
INSERT INTO transaction_pins (id,user_id,pin_hash,failed_attempts,locked_until,last_used_at,last_changed_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING *;
-- name: UpdatePINHash :execrows
UPDATE transaction_pins SET pin_hash=$2,failed_attempts=0,locked_until=NULL,last_changed_at=$3,updated_at=now() WHERE user_id=$1;
-- name: RecordPINFailure :exec
UPDATE transaction_pins SET failed_attempts=failed_attempts+1,locked_until=$2,updated_at=now() WHERE id=$1;
-- name: ResetPINAttempts :exec
UPDATE transaction_pins SET failed_attempts=0,locked_until=NULL,last_used_at=$2,updated_at=now() WHERE id=$1;
-- name: RecentOTPCount :one
SELECT count(*) FROM otp_codes WHERE user_id=$1 AND purpose=$2 AND created_at >= $3;
-- name: InvalidateOTPs :exec
UPDATE otp_codes SET consumed_at=$3 WHERE user_id=$1 AND purpose=$2 AND consumed_at IS NULL;
-- name: CreateOTP :one
INSERT INTO otp_codes (id,user_id,purpose,channel,destination,code_hash,challenge_token,failed_attempts,consumed_at,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING *;
-- name: OTPByChallengeToken :one
SELECT * FROM otp_codes WHERE challenge_token=$1;
-- name: RecordOTPFailure :exec
UPDATE otp_codes SET failed_attempts=failed_attempts+1 WHERE id=$1;
-- name: ConsumeOTP :execrows
UPDATE otp_codes SET consumed_at=$2 WHERE id=$1 AND consumed_at IS NULL AND expires_at>$2;
-- name: DeviceByFingerprint :one
SELECT * FROM user_devices WHERE user_id=$1 AND fingerprint=$2;
-- name: DeviceByID :one
SELECT * FROM user_devices WHERE id=$1;
-- name: TouchDevice :exec
UPDATE user_devices SET last_ip=$2,last_seen_at=$3,updated_at=now() WHERE id=$1;
-- name: UpsertDevice :one
INSERT INTO user_devices (id,user_id,fingerprint,name,platform,push_token,app_version,os_version,device_model,trusted,blocked,last_ip,latitude,longitude,last_seen_at,trusted_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) ON CONFLICT (user_id,fingerprint) DO UPDATE SET name=excluded.name,platform=excluded.platform,push_token=excluded.push_token,app_version=excluded.app_version,os_version=excluded.os_version,device_model=excluded.device_model,last_ip=excluded.last_ip,latitude=excluded.latitude,longitude=excluded.longitude,last_seen_at=excluded.last_seen_at,updated_at=now() RETURNING *;
-- name: DevicesByUserID :many
SELECT * FROM user_devices WHERE user_id=$1 ORDER BY last_seen_at DESC NULLS LAST;
-- name: SetDeviceTrusted :exec
UPDATE user_devices SET trusted=$2,trusted_at=$3,updated_at=now() WHERE id=$1;
-- name: SetDeviceBlocked :exec
UPDATE user_devices SET blocked=$2,updated_at=now() WHERE id=$1;
-- name: RecordRiskAssessment :one
INSERT INTO risk_assessments (id,user_id,operation,decision,score,triggered_rules,amount_minor,currency,device_id,ip_address,reference,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING *;
-- name: AssessmentsByUserID :many
SELECT * FROM risk_assessments WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2;
