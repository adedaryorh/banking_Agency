

CREATE TABLE IF NOT EXISTS transfer_pin_authorizations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL,
  amount_minor bigint NOT NULL CHECK(amount_minor>0),
  currency varchar(3) NOT NULL,
  recipient_descriptor text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz
);

-- Partial index over the live (unconsumed) tokens a user might present.
CREATE INDEX IF NOT EXISTS transfer_pin_authorizations_user_idx
  ON transfer_pin_authorizations(user_id,expires_at) WHERE consumed_at IS NULL;
