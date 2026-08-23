
CREATE TABLE IF NOT EXISTS funding_accounts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id    uuid NOT NULL,
    wallet_id      uuid NOT NULL REFERENCES wallets(id) ON DELETE RESTRICT,
    provider_name  text NOT NULL,
    provider_ref   text NOT NULL,


    account_number text NOT NULL,
    account_name   text NOT NULL,
    bank_name      text NOT NULL,
    bank_code      text NOT NULL DEFAULT '',
    currency       char(3) NOT NULL DEFAULT 'NGN',
    -- An account that expires is not a funding account. Recorded from what the
    -- provider actually said, so a checkout-style account that would silently
    -- stop accepting money is visible rather than assumed away.
    is_permanent   boolean NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now(),

    -- One funding account per customer per currency per provider: provisioning
    -- is idempotent, and a second account would split their incoming money
    -- across two numbers, only one of which the app shows them.
    CONSTRAINT uq_funding_accounts_customer
        UNIQUE (customer_id, currency, provider_name)
);

-- Two customers cannot be handed the same number.
CREATE UNIQUE INDEX IF NOT EXISTS uq_funding_accounts_number
    ON funding_accounts (provider_name, account_number);
CREATE INDEX IF NOT EXISTS idx_funding_accounts_customer
    ON funding_accounts (customer_id);

-- ---------------------------------------------------------------------------
-- Every collection we have accepted, recorded once.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS collections (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    funding_account_id    uuid NOT NULL REFERENCES funding_accounts(id) ON DELETE RESTRICT,
    customer_id           uuid NOT NULL,
    wallet_id             uuid NOT NULL REFERENCES wallets(id) ON DELETE RESTRICT,
    provider_name         text NOT NULL,
    provider_ref          text NOT NULL,
    amount_minor          bigint NOT NULL CHECK (amount_minor > 0),
    fee_minor             bigint NOT NULL DEFAULT 0 CHECK (fee_minor >= 0),
    currency              char(3) NOT NULL,
    sender_name           text NOT NULL DEFAULT '',
    narrative             text NOT NULL DEFAULT '',
    ledger_transaction_id uuid REFERENCES ledger_transactions(id),
    occurred_at           timestamptz NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),


    CONSTRAINT uq_collections_provider_ref UNIQUE (provider_name, provider_ref)
);

CREATE INDEX IF NOT EXISTS idx_collections_customer
    ON collections (customer_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_collections_account
    ON collections (funding_account_id, occurred_at DESC);


ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS request_id   varchar(100);
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS processed_at timestamptz;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS status       varchar(20) NOT NULL DEFAULT 'pending';
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS attempts     integer NOT NULL DEFAULT 0;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS last_error   text;
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS available_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS published_at timestamptz;

-- payload is jsonb in one definition and bytea in the other; Enqueue marshals
-- to JSON either way, so only a bytea column needs converting.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
         WHERE table_name = 'outbox_events' AND column_name = 'payload'
           AND data_type = 'bytea'
    ) THEN
        ALTER TABLE outbox_events
            ALTER COLUMN payload TYPE jsonb USING convert_from(payload, 'UTF8')::jsonb;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_outbox_events_unprocessed
    ON outbox_events (created_at) WHERE processed_at IS NULL;
