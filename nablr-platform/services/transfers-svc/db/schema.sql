CREATE EXTENSION IF NOT EXISTS pgcrypto;

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

CREATE TABLE IF NOT EXISTS wallets (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL, currency varchar(3) NOT NULL DEFAULT 'NGN',
  status varchar(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active','frozen','closed')),
  available_minor bigint NOT NULL DEFAULT 0 CHECK (available_minor >= 0), reserved_minor bigint NOT NULL DEFAULT 0 CHECK (reserved_minor >= 0),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(user_id,currency)
);
CREATE TABLE IF NOT EXISTS ledger_accounts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_type varchar(32) NOT NULL CHECK (account_type IN (
    'customer_asset','provider_clearing','fees','revenue','suspense','settlement','platform_adjustment'
  )),
  currency char(3) NOT NULL, owner_type varchar(24) NOT NULL DEFAULT 'customer', customer_id uuid, provider_name text,
  name text NOT NULL, code text NOT NULL, balance_minor bigint NOT NULL DEFAULT 0,
  reserved_minor bigint NOT NULL DEFAULT 0, pending_minor bigint NOT NULL DEFAULT 0,
  available_minor bigint GENERATED ALWAYS AS (balance_minor - reserved_minor) STORED,
  allows_negative boolean NOT NULL DEFAULT false,
  status varchar(16) NOT NULL DEFAULT 'active', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_accounts_code ON ledger_accounts (code);
CREATE INDEX IF NOT EXISTS idx_ledger_accounts_customer_currency
  ON ledger_accounts (customer_id, currency)
  WHERE owner_type = 'customer';
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS ledger_account_id uuid REFERENCES ledger_accounts(id);
-- The v1 wallet read model expects these wallet columns; they were added only in
-- the migration set (003) that can no longer run on a schema.sql-provisioned
-- database, so they are declared here idempotently.
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS account_id        uuid;
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS customer_id       uuid;
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS name              text;
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS wallet_type       varchar(24) NOT NULL DEFAULT 'spending';
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS is_default        boolean NOT NULL DEFAULT true;
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS wallets_ledger_account_id_fkey;
ALTER TABLE wallets ADD CONSTRAINT wallets_ledger_account_id_fkey FOREIGN KEY (ledger_account_id) REFERENCES ledger_accounts(id);
CREATE INDEX IF NOT EXISTS idx_wallets_customer ON wallets (customer_id, currency);
CREATE TABLE IF NOT EXISTS ledger_transactions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), reference varchar(96) NOT NULL UNIQUE, kind varchar(32) NOT NULL,
  status varchar(16) NOT NULL DEFAULT 'posted' CHECK (status IN ('posted','reversed')), idempotency_key varchar(128) UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  -- Posted by the platform LedgerService.PostTransaction; the sqlc engine
  -- omits them. Nullable so a row written by either engine satisfies the other.
  description text, posted_at timestamptz
);
CREATE TABLE IF NOT EXISTS ledger_entries (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), transaction_id uuid NOT NULL REFERENCES ledger_transactions(id),
  -- Two engines write this table. The sqlc engine addresses the customer's
  -- wallet (wallet_id + entry_type) and the platform engine addresses a ledger
  -- account (account_id + direction). Both must apply, so the two halves are
  -- nullable columns and each write populates only its own.
  wallet_id uuid REFERENCES wallets(id), entry_type varchar(6) CHECK (entry_type IN ('debit','credit')),
  account_id uuid REFERENCES ledger_accounts(id), direction varchar(6) CHECK (direction IN ('debit','credit')),
  amount_minor bigint NOT NULL CHECK (amount_minor > 0), currency varchar(3) NOT NULL,
  balance_after_minor bigint, description text,
  created_at timestamptz NOT NULL DEFAULT now()
);
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
CREATE INDEX IF NOT EXISTS ledger_entries_wallet_created_idx ON ledger_entries(wallet_id,created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_transaction
  ON ledger_entries (transaction_id);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_account_created
  ON ledger_entries (account_id, created_at DESC);
-- The customer-facing feed. Written by the wallet/funding/card services when money
-- moves; read by the wallet read endpoints (list transactions, statement, insights).
CREATE TABLE IF NOT EXISTS wallet_transactions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  wallet_id uuid REFERENCES wallets(id), customer_id uuid NOT NULL,
  ledger_transaction_id uuid REFERENCES ledger_transactions(id),
  direction varchar(10) NOT NULL CHECK (direction IN ('in', 'out')),
  amount_minor bigint NOT NULL CHECK (amount_minor > 0), currency char(3) NOT NULL,
  fee_minor bigint NOT NULL DEFAULT 0, balance_after_minor bigint,
  transaction_type varchar(50) NOT NULL, counterparty_name text, counterparty_type varchar(50),
  counterparty_id uuid, description text, source_type varchar(50), source_id uuid,
  occurred_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE wallet_transactions ALTER COLUMN id SET DEFAULT gen_random_uuid();
CREATE INDEX IF NOT EXISTS idx_wallet_tx_wallet_occurred ON wallet_transactions(wallet_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_wallet_transactions_customer_created
  ON wallet_transactions (customer_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_wallet_transactions_pagination
  ON wallet_transactions (wallet_id, occurred_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS holds (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), wallet_id uuid NOT NULL REFERENCES wallets(id), transfer_id uuid, amount_minor bigint NOT NULL CHECK(amount_minor>0),
  status varchar(16) NOT NULL DEFAULT 'active' CHECK(status IN ('active','captured','released')), expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(), resolved_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_holds_active_expiry
  ON holds (expires_at)
  WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_holds_wallet_created
  ON holds (wallet_id, created_at DESC);
-- customer_limits is versioned: one row per (user_id, limit_type, currency) is
-- active at a time (effective_to IS NULL).

CREATE TABLE IF NOT EXISTS customer_limits (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL,
  limit_type varchar(24) NOT NULL CHECK (limit_type IN (
    'per_transaction','daily_outbound','weekly_outbound','monthly_outbound',
    'balance_cap','daily_count','atm_daily','card_daily','international_daily')),
  currency varchar(3) NOT NULL,
  amount_minor bigint CHECK (amount_minor IS NULL OR amount_minor >= 0),
  count_limit integer CHECK (count_limit IS NULL OR count_limit >= 0),
  source varchar(16) NOT NULL DEFAULT 'tier' CHECK (source IN ('tier','override','risk')),
  reason text,
  effective_from timestamptz NOT NULL DEFAULT now(),
  effective_to timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT customer_limits_value CHECK (amount_minor IS NOT NULL OR count_limit IS NOT NULL),
  CONSTRAINT customer_limits_override_reason CHECK (source <> 'override' OR reason IS NOT NULL)
);

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
    'balance_cap','daily_count','atm_daily','card_daily','international_daily')),
  ADD CONSTRAINT customer_limits_amount_minor_check CHECK (amount_minor IS NULL OR amount_minor >= 0),
  ADD CONSTRAINT customer_limits_count_limit_check CHECK (count_limit IS NULL OR count_limit >= 0),
  ADD CONSTRAINT customer_limits_source_check CHECK (source IN ('tier','override','risk')),
  ADD CONSTRAINT customer_limits_value CHECK (amount_minor IS NOT NULL OR count_limit IS NOT NULL),
  ADD CONSTRAINT customer_limits_override_reason CHECK (source <> 'override' OR reason IS NOT NULL);
DROP INDEX IF EXISTS idx_customer_limits_lookup;
CREATE UNIQUE INDEX IF NOT EXISTS uq_customer_limits_active
  ON customer_limits (user_id, limit_type, currency) WHERE effective_to IS NULL;
CREATE INDEX IF NOT EXISTS idx_customer_limits_user ON customer_limits (user_id);
CREATE TABLE IF NOT EXISTS beneficiaries (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL, type varchar(20) NOT NULL CHECK(type IN ('internal','bank')),
  recipient_user_id uuid, bank_code varchar(32), bank_name varchar(160),
  account_name varchar(160) NOT NULL, currency varchar(3) NOT NULL DEFAULT 'NGN',
  -- Account numbers are never stored in plaintext: the blind index feeds the
  account_number_ciphertext bytea, account_number_last4 varchar(4) CHECK (account_number_last4 IS NULL OR account_number_last4 ~ '^[0-9]{4}$'),
  account_number_blind_index bytea, key_version smallint,
  verification_status varchar(32) NOT NULL DEFAULT 'unverified' CHECK (verification_status IN ('unverified','pending','verified','name_mismatch','partial_match','failed','not_supported')),
  verified_name varchar(160), verified_at timestamptz, verification_provider varchar(64),
  nickname varchar(60), is_favourite boolean NOT NULL DEFAULT false, is_saved boolean NOT NULL DEFAULT true,
  cooling_period_ends_at timestamptz, last_used_at timestamptz, use_count integer NOT NULL DEFAULT 0,
  status varchar(16) NOT NULL DEFAULT 'active' CHECK(status IN ('active','blocked','deleted')),
  deleted_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((type='internal' AND recipient_user_id IS NOT NULL) OR (type='bank' AND bank_code IS NOT NULL))
);
-- Capture of the recipient's Nablr handle / account number at bind time, for internal payees.
ALTER TABLE beneficiaries ADD COLUMN IF NOT EXISTS recipient_account_number text;
ALTER TABLE beneficiaries ADD COLUMN IF NOT EXISTS recipient_username text;
CREATE INDEX IF NOT EXISTS beneficiaries_user_idx ON beneficiaries(user_id,created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS uq_beneficiaries_account ON beneficiaries (user_id, account_number_blind_index, bank_code) WHERE account_number_blind_index IS NOT NULL AND status <> 'deleted';
CREATE UNIQUE INDEX IF NOT EXISTS uq_beneficiaries_internal ON beneficiaries (user_id, recipient_user_id) WHERE recipient_user_id IS NOT NULL AND status <> 'deleted';
CREATE INDEX IF NOT EXISTS idx_beneficiaries_saved ON beneficiaries (user_id, last_used_at DESC NULLS LAST) WHERE is_saved AND status <> 'deleted';
CREATE INDEX IF NOT EXISTS idx_beneficiaries_favourite ON beneficiaries (user_id) WHERE is_favourite;
-- Display identifiers for an internal (nablr→nablr) payee, captured at bind time
ALTER TABLE beneficiaries ADD COLUMN IF NOT EXISTS recipient_account_number varchar(16);
ALTER TABLE beneficiaries ADD COLUMN IF NOT EXISTS recipient_username varchar(64);
-- Object-storage key of the internal payee's profile image, captured at bind
ALTER TABLE beneficiaries ADD COLUMN IF NOT EXISTS recipient_avatar_url text;
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
CREATE TABLE IF NOT EXISTS transfers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), reference varchar(96) NOT NULL UNIQUE, sender_user_id uuid NOT NULL, source_wallet_id uuid NOT NULL REFERENCES wallets(id),
  beneficiary_id uuid NOT NULL REFERENCES beneficiaries(id), destination_wallet_id uuid REFERENCES wallets(id), type varchar(20) NOT NULL CHECK(type IN ('internal','bank')),
  send_amount_minor bigint NOT NULL CHECK(send_amount_minor>0), send_currency varchar(3) NOT NULL, receive_amount_minor bigint NOT NULL CHECK(receive_amount_minor>0), receive_currency varchar(3) NOT NULL,
  fee_minor bigint NOT NULL DEFAULT 0 CHECK(fee_minor>=0), fee_currency varchar(3), total_debit_minor bigint NOT NULL CHECK(total_debit_minor>0),
  fx_quote_id uuid, status varchar(16) NOT NULL CHECK(status IN ('created','pending','processing','under_review','completed','failed','reversed','refunded','cancelled')),
  failure_code varchar(64), failure_reason text, hold_id uuid REFERENCES holds(id), ledger_transaction_id uuid REFERENCES ledger_transactions(id),
  provider_name varchar(64), provider_reference varchar(160), submitted_at timestamptz, completed_at timestamptz,
  idempotency_key varchar(128) NOT NULL, narrative text, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE transfers ALTER COLUMN id SET DEFAULT gen_random_uuid();
ALTER TABLE transfers DROP CONSTRAINT IF EXISTS transfers_completion;
-- Compatibility for databases created from the earlier migration file. That.
DO $$
DECLARE
  constraint_name text;
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'transfers' AND column_name = 'customer_id'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'transfers' AND column_name = 'sender_user_id'
  ) THEN
    EXECUTE 'ALTER TABLE transfers RENAME COLUMN customer_id TO sender_user_id';
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'transfers' AND column_name = 'transfer_type'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'transfers' AND column_name = 'type'
  ) THEN
    EXECUTE 'ALTER TABLE transfers RENAME COLUMN transfer_type TO type';
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'transfers' AND column_name = 'type'
  ) THEN
    FOR constraint_name IN
      SELECT c.conname
      FROM pg_constraint c
      JOIN pg_class t ON t.oid = c.conrelid
      JOIN pg_namespace n ON n.oid = t.relnamespace
      WHERE n.nspname = 'public'
        AND t.relname = 'transfers'
        AND c.contype = 'c'
        AND pg_get_constraintdef(c.oid) LIKE '%local_bank%'
    LOOP
      EXECUTE format('ALTER TABLE transfers DROP CONSTRAINT IF EXISTS %I', constraint_name);
    END LOOP;
    UPDATE transfers SET type = 'bank' WHERE type IN ('local_bank', 'international', 'remittance');
  END IF;
END $$;
ALTER TABLE transfers DROP CONSTRAINT IF EXISTS transfers_type_check;
ALTER TABLE transfers
  ADD CONSTRAINT transfers_type_check
  CHECK (type IN ('internal', 'bank'))
  NOT VALID;
ALTER TABLE transfers ADD COLUMN IF NOT EXISTS reversal_transaction_id uuid REFERENCES ledger_transactions(id);
ALTER TABLE transfers ADD COLUMN IF NOT EXISTS expected_settlement_at timestamptz;
-- Idempotency is scoped to the SENDER, matching TransferByKey's lookup on
-- (sender_user_id, idempotency_key).
--

ALTER TABLE transfers DROP CONSTRAINT IF EXISTS transfers_idempotency_key_key;
CREATE UNIQUE INDEX IF NOT EXISTS transfers_sender_idempotency_idx ON transfers(sender_user_id,idempotency_key);
CREATE UNIQUE INDEX IF NOT EXISTS transfers_provider_reference_idx ON transfers(provider_name,provider_reference) WHERE provider_reference IS NOT NULL;
CREATE INDEX IF NOT EXISTS transfers_sender_created_idx ON transfers(sender_user_id,created_at DESC);
CREATE INDEX IF NOT EXISTS idx_transfers_status ON transfers (status, created_at) WHERE status IN ('created', 'pending', 'processing', 'under_review');
CREATE INDEX IF NOT EXISTS idx_transfers_beneficiary ON transfers (beneficiary_id) WHERE beneficiary_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_transfers_processing_submitted
  ON transfers (submitted_at)
  WHERE status = 'processing' AND provider_reference IS NOT NULL;

-- Transfer state is reconstructable and append-only.  A provider callback or
-- worker retry must never silently overwrite the history of a money movement.
CREATE TABLE IF NOT EXISTS transfer_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), transfer_id uuid NOT NULL REFERENCES transfers(id),
  from_status varchar(16), to_status varchar(16) NOT NULL, actor_type varchar(16) NOT NULL CHECK(actor_type IN ('user','system','provider','admin','scheduler')),
  actor_user_id uuid, reason text, provider_payload jsonb, created_at timestamptz NOT NULL DEFAULT now()
);
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
ALTER TABLE transfer_events DROP CONSTRAINT IF EXISTS transfer_events_actor_type_check;
ALTER TABLE transfer_events ADD CONSTRAINT transfer_events_actor_type_check
  CHECK (actor_type IN ('user','system','provider','admin','scheduler'));
CREATE INDEX IF NOT EXISTS transfer_events_transfer_idx ON transfer_events(transfer_id,created_at);

CREATE TABLE IF NOT EXISTS transfer_quotes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL, source_wallet_id uuid NOT NULL REFERENCES wallets(id),
  beneficiary_id uuid NOT NULL REFERENCES beneficiaries(id), type varchar(20) NOT NULL, send_amount_minor bigint NOT NULL CHECK(send_amount_minor>0),
  send_currency varchar(3) NOT NULL, receive_amount_minor bigint NOT NULL CHECK(receive_amount_minor>0), receive_currency varchar(3) NOT NULL,
  fee_minor bigint NOT NULL DEFAULT 0 CHECK(fee_minor>=0), fee_currency varchar(3), total_debit_minor bigint NOT NULL CHECK(total_debit_minor>0),
  fx_quote_id uuid, rate_numerator bigint, rate_scale smallint,
  high_value boolean NOT NULL DEFAULT false, high_value_note text, status varchar(16) NOT NULL DEFAULT 'active' CHECK(status IN ('active','used','expired','cancelled')),
  expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS transfer_quotes_active_idx ON transfer_quotes(user_id,expires_at) WHERE status='active';

-- A transfer_pin_authorization is the server-side proof that the payer entered

CREATE TABLE IF NOT EXISTS transfer_pin_authorizations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL,
  amount_minor bigint NOT NULL CHECK(amount_minor>0), currency varchar(3) NOT NULL,
  recipient_descriptor text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL,
  consumed_at timestamptz
);
CREATE INDEX IF NOT EXISTS transfer_pin_authorizations_user_idx ON transfer_pin_authorizations(user_id,expires_at) WHERE consumed_at IS NULL;

-- A "request money" is one nablr user asking another to pay them; it moves NO

CREATE TABLE IF NOT EXISTS payment_requests (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  requester_user_id uuid NOT NULL, payer_user_id uuid NOT NULL,
  amount_minor bigint NOT NULL CHECK(amount_minor>0), currency varchar(3) NOT NULL DEFAULT 'NGN',
  note text,
  status varchar(16) NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','paid','declined','cancelled','expired')),
  transfer_id uuid REFERENCES transfers(id),
  requester_name varchar(160), requester_username varchar(64),
  payer_name varchar(160), payer_username varchar(64),
  decline_reason text, expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (requester_user_id <> payer_user_id)
);
CREATE INDEX IF NOT EXISTS payment_requests_payer_idx ON payment_requests(payer_user_id,created_at DESC);
CREATE INDEX IF NOT EXISTS payment_requests_requester_idx ON payment_requests(requester_user_id,created_at DESC);

CREATE TABLE IF NOT EXISTS scheduled_payments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), user_id uuid NOT NULL, source_wallet_id uuid NOT NULL REFERENCES wallets(id), beneficiary_id uuid NOT NULL REFERENCES beneficiaries(id),
  payment_name varchar(120) NOT NULL DEFAULT 'Scheduled payment',
  schedule_type varchar(16) NOT NULL CHECK(schedule_type IN ('one_off','recurring')), frequency varchar(20), day_of_month smallint CHECK(day_of_month BETWEEN 1 AND 31),
  day_of_week smallint CHECK(day_of_week BETWEEN 0 AND 6), amount_minor bigint NOT NULL CHECK(amount_minor>0), currency varchar(3) NOT NULL,
  narrative text, next_run_at timestamptz NOT NULL, last_run_at timestamptz, end_date timestamptz, max_occurrences integer, occurrence_count integer NOT NULL DEFAULT 0,
  consecutive_failures integer NOT NULL DEFAULT 0, status varchar(16) NOT NULL DEFAULT 'active' CHECK(status IN ('active','paused','completed','cancelled','failed')),
  pause_reason text, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE scheduled_payments ALTER COLUMN id SET DEFAULT gen_random_uuid();
ALTER TABLE scheduled_payments ADD COLUMN IF NOT EXISTS payment_name varchar(120) NOT NULL DEFAULT 'Scheduled payment';
-- Compatibility for the earlier scheduled_payments schema, which used
-- customer_id. Current sqlc queries use user_id.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'scheduled_payments' AND column_name = 'customer_id'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'scheduled_payments' AND column_name = 'user_id'
  ) THEN
    EXECUTE 'ALTER TABLE scheduled_payments RENAME COLUMN customer_id TO user_id';
  END IF;
END $$;
CREATE INDEX IF NOT EXISTS scheduled_payments_due_idx ON scheduled_payments(next_run_at) WHERE status='active';
CREATE INDEX IF NOT EXISTS scheduled_payments_customer_idx ON scheduled_payments (user_id, status);
CREATE TABLE IF NOT EXISTS scheduled_payment_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), scheduled_payment_id uuid NOT NULL REFERENCES scheduled_payments(id), scheduled_for timestamptz NOT NULL,
  executed_at timestamptz NOT NULL DEFAULT now(), outcome varchar(32) NOT NULL CHECK(outcome IN ('succeeded','insufficient_funds','failed','skipped','cancelled')),
  transfer_id uuid REFERENCES transfers(id), error_message text,
  UNIQUE(scheduled_payment_id,scheduled_for)
);

CREATE TABLE IF NOT EXISTS outbox_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), aggregate_type varchar(32) NOT NULL, aggregate_id uuid NOT NULL, event_type varchar(96) NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb, status varchar(16) NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','processing','published','failed','cancelled')),
  attempts integer NOT NULL DEFAULT 0, last_error text, available_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz
);
CREATE INDEX IF NOT EXISTS outbox_events_ready_idx ON outbox_events(available_at,id) WHERE status IN ('pending','failed');

CREATE TABLE IF NOT EXISTS provider_requests (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), transfer_id uuid NOT NULL REFERENCES transfers(id), provider_name varchar(64) NOT NULL,
  provider_reference varchar(160), idempotency_key varchar(128) NOT NULL, outcome varchar(24) NOT NULL, error_code varchar(64), error_message text,
  created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(provider_name,idempotency_key)
);
CREATE TABLE IF NOT EXISTS provider_webhooks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), provider_name varchar(64) NOT NULL, event_id varchar(160) NOT NULL,
  event_type varchar(64) NOT NULL, raw_body_sha256 varchar(64) NOT NULL, payload jsonb NOT NULL,
  signature_valid boolean NOT NULL, status varchar(16) NOT NULL DEFAULT 'received' CHECK(status IN ('received','processed','failed','replayed','ignored')), processed_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
  processing_attempts smallint NOT NULL DEFAULT 0, error_message text, replayed_from_id uuid, replayed_by uuid,
  UNIQUE(provider_name,event_id)
);
CREATE TABLE IF NOT EXISTS banks (
  code text PRIMARY KEY, name text NOT NULL,
  non_interest boolean NOT NULL DEFAULT false, is_active boolean NOT NULL DEFAULT true, novac_code text,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS reconciliation_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), provider_name varchar(64) NOT NULL,
  reconciliation_type varchar(24) NOT NULL DEFAULT 'payout', period_start timestamptz NOT NULL, period_end timestamptz NOT NULL,
  currency char(3) NOT NULL DEFAULT 'NGN', status varchar(24) NOT NULL DEFAULT 'running',
  provider_item_count int NOT NULL DEFAULT 0, ledger_item_count int NOT NULL DEFAULT 0, matched_count int NOT NULL DEFAULT 0, break_count int NOT NULL DEFAULT 0,
  provider_total_minor bigint NOT NULL DEFAULT 0, ledger_total_minor bigint NOT NULL DEFAULT 0, variance_minor bigint NOT NULL DEFAULT 0,
  triggered_by uuid, created_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz
);
CREATE TABLE IF NOT EXISTS reconciliation_items (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), run_id uuid NOT NULL REFERENCES reconciliation_runs(id) ON DELETE CASCADE,
  match_status varchar(32) NOT NULL, provider_reference varchar(160), our_reference varchar(96),
  provider_amount_minor bigint, provider_status varchar(32), provider_timestamp timestamptz, provider_payload jsonb,
  ledger_transaction_id uuid, ledger_amount_minor bigint, currency char(3),
  variance_minor bigint NOT NULL DEFAULT 0, resolution_status varchar(24), resolution_note text,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS round_up_accruals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), customer_id uuid NOT NULL, currency char(3) NOT NULL,
  round_up_multiplier smallint NOT NULL DEFAULT 1, accrued_minor bigint NOT NULL DEFAULT 0,
  is_active boolean NOT NULL DEFAULT true, sweep_threshold_minor bigint NOT NULL DEFAULT 500, last_swept_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS round_up_items (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(), accrual_id uuid NOT NULL,
  wallet_transaction_id uuid NOT NULL UNIQUE, spend_amount_minor bigint NOT NULL, round_up_minor bigint NOT NULL,
  swept_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS feature_flags (
  key text PRIMARY KEY,
  enabled boolean NOT NULL,
  note text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now()
);
