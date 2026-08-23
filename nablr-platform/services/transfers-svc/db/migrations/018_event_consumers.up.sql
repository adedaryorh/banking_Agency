CREATE TABLE IF NOT EXISTS consumed_events (
  consumer_name varchar(120) NOT NULL,
  event_id uuid NOT NULL,
  event_type varchar(160) NOT NULL,
  status varchar(20) NOT NULL DEFAULT 'processing',
  attempts integer NOT NULL DEFAULT 1,
  last_error text,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz,
  PRIMARY KEY (consumer_name, event_id)
);
CREATE INDEX IF NOT EXISTS idx_consumed_events_failed
  ON consumed_events(status, first_seen_at) WHERE status = 'failed';

CREATE TABLE IF NOT EXISTS dead_letter_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  consumer_name varchar(120) NOT NULL,
  event_id uuid NOT NULL,
  event_type varchar(160) NOT NULL,
  payload jsonb NOT NULL,
  error_message text NOT NULL,
  attempts integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  replayed_at timestamptz,
  UNIQUE (consumer_name, event_id)
);
