-- name: CreateNotificationTemplate :one
INSERT INTO notification_templates (
    id,
    code,
    channel,
    subject,
    body_text,
    body_html,
    locale,
    active
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING *;

-- name: NotificationTemplateByCode :one
SELECT *
FROM notification_templates
WHERE code = $1
  AND channel = $2
  AND locale = $3
  AND active = true;

-- name: ListNotificationTemplates :many
SELECT *
FROM notification_templates
WHERE (sqlc.narg(channel)::text IS NULL OR channel = sqlc.narg(channel))
  AND (sqlc.narg(locale)::text IS NULL OR locale = sqlc.narg(locale))
  AND (sqlc.narg(active)::boolean IS NULL OR active = sqlc.narg(active))
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: UpdateNotificationTemplate :one
UPDATE notification_templates
SET
    code = $2,
    channel = $3,
    subject = $4,
    body_text = $5,
    body_html = $6,
    locale = $7,
    active = $8,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpsertNotificationPreference :one
INSERT INTO notification_preferences (
    id,
    user_id,
    channel,
    category,
    enabled
) VALUES (
    $1, $2, $3, $4, $5
)
ON CONFLICT (user_id, channel, category)
DO UPDATE SET
    enabled = EXCLUDED.enabled,
    updated_at = now()
RETURNING *;

-- name: NotificationPreference :one
SELECT *
FROM notification_preferences
WHERE user_id = $1
  AND channel = $2
  AND category = $3;

-- name: ListNotificationPreferencesByUser :many
SELECT *
FROM notification_preferences
WHERE user_id = $1
ORDER BY channel, category;

-- name: UpsertDeviceToken :one
INSERT INTO device_tokens (
    id,
    user_id,
    token_hash,
    provider,
    platform,
    last_used_at,
    revoked_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (token_hash)
DO UPDATE SET
    user_id = EXCLUDED.user_id,
    provider = EXCLUDED.provider,
    platform = EXCLUDED.platform,
    last_used_at = COALESCE(EXCLUDED.last_used_at, now()),
    revoked_at = NULL,
    updated_at = now()
RETURNING *;

-- name: ActiveDeviceTokensByUser :many
SELECT *
FROM device_tokens
WHERE user_id = $1
  AND revoked_at IS NULL
ORDER BY COALESCE(last_used_at, created_at) DESC;

-- name: RevokeDeviceToken :execrows
UPDATE device_tokens
SET revoked_at = now(),
    updated_at = now()
WHERE token_hash = $1
  AND revoked_at IS NULL;

-- name: CreateDeliveryAttempt :one
INSERT INTO notification_delivery_attempts (
    id,
    request_id,
    idempotency_key,
    user_id,
    channel,
    provider,
    template_code,
    destination_hash,
    destination_masked,
    subject,
    status,
    provider_reference,
    error_code,
    error_message,
    payload,
    sent_at,
    failed_at,
    next_retry_at,
    attempt_count
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
    $11, $12, $13, $14, $15, $16, $17, $18, $19
) RETURNING *;

-- name: DeliveryAttemptByIdempotencyKey :one
SELECT *
FROM notification_delivery_attempts
WHERE idempotency_key = $1;

-- name: DeliveryAttemptByID :one
SELECT *
FROM notification_delivery_attempts
WHERE id = $1;

-- name: ListDeliveryAttemptsByUser :many
SELECT *
FROM notification_delivery_attempts
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: MarkDeliveryAttemptSending :one
UPDATE notification_delivery_attempts
SET status = 'sending',
    attempt_count = attempt_count + 1,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkDeliveryAttemptSent :one
UPDATE notification_delivery_attempts
SET status = 'sent',
    provider_reference = $2,
    sent_at = now(),
    error_code = NULL,
    error_message = NULL,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkDeliveryAttemptFailed :one
UPDATE notification_delivery_attempts
SET status = $2,
    error_code = $3,
    error_message = $4,
    failed_at = now(),
    next_retry_at = $5,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ClaimRetryableDeliveryAttempts :many
SELECT *
FROM notification_delivery_attempts
WHERE status IN ('queued', 'retrying')
  AND (next_retry_at IS NULL OR next_retry_at <= $1)
ORDER BY created_at
LIMIT $2
FOR UPDATE SKIP LOCKED;

-- name: UpsertProviderStatus :one
INSERT INTO notification_provider_status (
    provider,
    channel,
    status,
    success_count,
    failure_count,
    last_success_at,
    last_failure_at,
    last_error
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
ON CONFLICT (provider, channel)
DO UPDATE SET
    status = EXCLUDED.status,
    success_count = notification_provider_status.success_count + EXCLUDED.success_count,
    failure_count = notification_provider_status.failure_count + EXCLUDED.failure_count,
    last_success_at = COALESCE(EXCLUDED.last_success_at, notification_provider_status.last_success_at),
    last_failure_at = COALESCE(EXCLUDED.last_failure_at, notification_provider_status.last_failure_at),
    last_error = EXCLUDED.last_error,
    updated_at = now()
RETURNING *;

-- name: ProviderStatus :one
SELECT *
FROM notification_provider_status
WHERE provider = $1
  AND channel = $2;

-- name: CreateNotificationEvent :one
INSERT INTO notification_events (
    id,
    aggregate_id,
    event_type,
    payload
) VALUES (
    $1, $2, $3, $4
) RETURNING *;

-- name: ClaimUnpublishedNotificationEvents :many
WITH claimed AS (
    SELECT id FROM notification_events
    WHERE status IN ('pending', 'failed') AND available_at <= now()
    ORDER BY available_at, created_at
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
UPDATE notification_events event
SET status = 'processing', attempts = attempts + 1
FROM claimed
WHERE event.id = claimed.id
RETURNING event.*;

-- name: MarkNotificationEventPublished :execrows
UPDATE notification_events
SET status = 'published', published_at = now(), last_error = NULL
WHERE id = $1
  AND published_at IS NULL;

-- name: MarkNotificationEventFailed :exec
UPDATE notification_events
SET status = 'failed', last_error = $2, available_at = $3
WHERE id = $1;

-- name: DeadLetterNotificationEvent :exec
UPDATE notification_events
SET status = 'dead_letter', last_error = $2
WHERE id = $1;
