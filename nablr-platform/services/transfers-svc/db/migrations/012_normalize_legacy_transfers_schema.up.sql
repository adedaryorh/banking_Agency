DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'beneficiaries' AND column_name = 'customer_id'
  ) THEN
    IF EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public' AND table_name = 'beneficiaries' AND column_name = 'user_id'
    ) THEN
      EXECUTE 'UPDATE beneficiaries SET user_id = customer_id WHERE user_id IS NULL AND customer_id IS NOT NULL';
    END IF;
    EXECUTE 'ALTER TABLE beneficiaries ALTER COLUMN customer_id DROP NOT NULL';
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'beneficiaries' AND column_name = 'beneficiary_type'
  ) THEN
    EXECUTE 'ALTER TABLE beneficiaries ALTER COLUMN beneficiary_type DROP NOT NULL';
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'beneficiaries' AND column_name = 'display_name'
  ) THEN
    EXECUTE 'ALTER TABLE beneficiaries ALTER COLUMN display_name DROP NOT NULL';
  END IF;
END $$;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'customer_limits'
       AND column_name = 'customer_id'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'customer_limits'
       AND column_name = 'user_id'
  ) THEN
    EXECUTE 'ALTER TABLE customer_limits RENAME COLUMN customer_id TO user_id';
  END IF;
END $$;

ALTER TABLE customer_limits
  ADD COLUMN IF NOT EXISTS count_limit integer,
  ADD COLUMN IF NOT EXISTS source varchar(16) NOT NULL DEFAULT 'tier',
  ADD COLUMN IF NOT EXISTS reason text;
ALTER TABLE customer_limits ALTER COLUMN amount_minor DROP NOT NULL;
ALTER TABLE customer_limits
  DROP CONSTRAINT IF EXISTS customer_limits_limit_type_check,
  DROP CONSTRAINT IF EXISTS customer_limits_amount_minor_check,
  DROP CONSTRAINT IF EXISTS customer_limits_count_limit_check,
  DROP CONSTRAINT IF EXISTS customer_limits_source_check,
  DROP CONSTRAINT IF EXISTS customer_limits_value,
  DROP CONSTRAINT IF EXISTS customer_limits_override_reason;
ALTER TABLE customer_limits
  ADD CONSTRAINT customer_limits_limit_type_check CHECK (limit_type IN (
    'per_transaction','daily_outbound','weekly_outbound','monthly_outbound',
    'balance_cap','daily_count','atm_daily','card_daily','international_daily'
  )),
  ADD CONSTRAINT customer_limits_amount_minor_check
    CHECK (amount_minor IS NULL OR amount_minor >= 0),
  ADD CONSTRAINT customer_limits_count_limit_check
    CHECK (count_limit IS NULL OR count_limit >= 0),
  ADD CONSTRAINT customer_limits_source_check
    CHECK (source IN ('tier','override','risk')),
  ADD CONSTRAINT customer_limits_value
    CHECK (amount_minor IS NOT NULL OR count_limit IS NOT NULL),
  ADD CONSTRAINT customer_limits_override_reason
    CHECK (source <> 'override' OR reason IS NOT NULL);
DROP INDEX IF EXISTS idx_customer_limits_lookup;
CREATE UNIQUE INDEX IF NOT EXISTS uq_customer_limits_active
  ON customer_limits (user_id, limit_type, currency)
  WHERE effective_to IS NULL;
CREATE INDEX IF NOT EXISTS idx_customer_limits_user
  ON customer_limits (user_id);

ALTER TABLE transfers
  ALTER COLUMN id SET DEFAULT gen_random_uuid(),
  DROP CONSTRAINT IF EXISTS transfers_completion;
ALTER TABLE scheduled_payments
  ALTER COLUMN id SET DEFAULT gen_random_uuid();
ALTER TABLE wallet_transactions
  ALTER COLUMN id SET DEFAULT gen_random_uuid();
ALTER TABLE transfer_events
  ALTER COLUMN from_status DROP NOT NULL;

DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'transfer_events'
       AND column_name = 'id' AND data_type <> 'uuid'
  ) THEN
    ALTER TABLE transfer_events ALTER COLUMN id DROP DEFAULT;
    ALTER TABLE transfer_events ALTER COLUMN id TYPE uuid USING gen_random_uuid();
  END IF;
END $$;

ALTER TABLE transfer_events ALTER COLUMN id SET DEFAULT gen_random_uuid();
ALTER TABLE transfer_events
  DROP CONSTRAINT IF EXISTS transfer_events_actor_type_check;
ALTER TABLE transfer_events
  ADD CONSTRAINT transfer_events_actor_type_check
  CHECK (actor_type IN ('user','system','provider','admin','scheduler'));

CREATE OR REPLACE FUNCTION deny_ledger_entry_mutation()
RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'ledger entries are immutable; post a reversal entry instead';
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_ledger_entries_immutable ON ledger_entries;
CREATE TRIGGER trg_ledger_entries_immutable
BEFORE UPDATE OR DELETE ON ledger_entries
FOR EACH ROW EXECUTE FUNCTION deny_ledger_entry_mutation();

CREATE INDEX IF NOT EXISTS idx_ledger_accounts_customer_currency
  ON ledger_accounts (customer_id, currency)
  WHERE owner_type = 'customer';
CREATE INDEX IF NOT EXISTS idx_ledger_entries_transaction
  ON ledger_entries (transaction_id);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_account_created
  ON ledger_entries (account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_wallet_transactions_customer_created
  ON wallet_transactions (customer_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_wallet_transactions_pagination
  ON wallet_transactions (wallet_id, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_holds_active_expiry
  ON holds (expires_at)
  WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_holds_wallet_created
  ON holds (wallet_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_transfers_processing_submitted
  ON transfers (submitted_at)
  WHERE status = 'processing' AND provider_reference IS NOT NULL;
