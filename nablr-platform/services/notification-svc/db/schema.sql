CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS notification_templates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code varchar(120) NOT NULL UNIQUE,
    channel varchar(20) NOT NULL,
    subject varchar(200),
    body_text text,
    body_html text,
    locale varchar(20) NOT NULL DEFAULT 'en-NG',
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (channel IN ('sms','email','push')),
    CHECK (
        (channel = 'email' AND (subject IS NOT NULL OR body_html IS NOT NULL OR body_text IS NOT NULL))
        OR (channel IN ('sms','push') AND body_text IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS notification_preferences (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    channel varchar(20) NOT NULL,
    category varchar(80) NOT NULL DEFAULT 'transactional',
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, channel, category),
    CHECK (channel IN ('sms','email','push')),
    CHECK (category IN ('transactional','security','marketing','system'))
);

CREATE TABLE IF NOT EXISTS device_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    token_hash varchar(128) NOT NULL UNIQUE,
    provider varchar(40) NOT NULL DEFAULT 'firebase',
    platform varchar(30),
    last_used_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (provider IN ('firebase','apns','webpush','log')),
    CHECK (platform IS NULL OR platform IN ('ios','android','web'))
);

CREATE TABLE IF NOT EXISTS notification_delivery_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id varchar(120),
    idempotency_key varchar(160),
    user_id uuid,
    channel varchar(20) NOT NULL,
    provider varchar(40) NOT NULL,
    template_code varchar(120),
    destination_hash varchar(128),
    destination_masked varchar(120),
    subject varchar(200),
    status varchar(30) NOT NULL DEFAULT 'queued',
    provider_reference varchar(200),
    error_code varchar(80),
    error_message text,
    payload jsonb,
    sent_at timestamptz,
    failed_at timestamptz,
    next_retry_at timestamptz,
    attempt_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (channel IN ('sms','email','push')),
    CHECK (status IN ('queued','sending','sent','failed','retrying','cancelled')),
    CHECK (attempt_count >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_attempts_idempotency
    ON notification_delivery_attempts(idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_notification_attempts_user_created
    ON notification_delivery_attempts(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_attempts_status_retry
    ON notification_delivery_attempts(status, next_retry_at)
    WHERE status IN ('queued','retrying');
CREATE INDEX IF NOT EXISTS idx_notification_attempts_provider_ref
    ON notification_delivery_attempts(provider, provider_reference)
    WHERE provider_reference IS NOT NULL;

CREATE TABLE IF NOT EXISTS notification_provider_status (
    provider varchar(40) NOT NULL,
    channel varchar(20) NOT NULL,
    status varchar(30) NOT NULL DEFAULT 'unknown',
    success_count bigint NOT NULL DEFAULT 0,
    failure_count bigint NOT NULL DEFAULT 0,
    last_success_at timestamptz,
    last_failure_at timestamptz,
    last_error text,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, channel),
    CHECK (channel IN ('sms','email','push')),
    CHECK (status IN ('unknown','healthy','degraded','down'))
);

CREATE TABLE IF NOT EXISTS notification_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id uuid,
    event_type varchar(120) NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
	status varchar(20) NOT NULL DEFAULT 'pending',
	attempts integer NOT NULL DEFAULT 0,
	last_error text,
	available_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_notification_events_unpublished
    ON notification_events(available_at, created_at)
    WHERE status IN ('pending', 'failed');
