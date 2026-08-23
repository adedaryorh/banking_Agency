ALTER TABLE notification_events
    ADD COLUMN IF NOT EXISTS status varchar(20) NOT NULL DEFAULT 'pending',
    ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_error text,
    ADD COLUMN IF NOT EXISTS available_at timestamptz NOT NULL DEFAULT now();

DROP INDEX IF EXISTS idx_notification_events_unpublished;
CREATE INDEX IF NOT EXISTS idx_notification_events_ready
    ON notification_events(available_at, created_at)
    WHERE status IN ('pending', 'failed');
