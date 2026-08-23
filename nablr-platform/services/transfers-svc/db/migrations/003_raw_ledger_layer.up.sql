
CREATE TABLE IF NOT EXISTS ledger_accounts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_type    varchar(32) NOT NULL CHECK (account_type IN (
                        'customer_asset', 'provider_clearing', 'fees', 'revenue',
                        'suspense', 'settlement'
                    )),
    currency        char(3) NOT NULL,
    owner_type      varchar(24) NOT NULL DEFAULT 'customer'
                    CHECK (owner_type IN ('customer', 'platform', 'provider')),
    customer_id     uuid,
    provider_name   text,
    name            text NOT NULL,

    code            text NOT NULL,
    balance_minor   bigint NOT NULL DEFAULT 0,
    reserved_minor  bigint NOT NULL DEFAULT 0,
    pending_minor   bigint NOT NULL DEFAULT 0,
    available_minor bigint NOT NULL DEFAULT 0,
    status          varchar(16) NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'frozen', 'closed')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_accounts_code ON ledger_accounts (code);
CREATE INDEX IF NOT EXISTS idx_ledger_accounts_customer
    ON ledger_accounts (customer_id, currency) WHERE customer_id IS NOT NULL;


ALTER TABLE holds ADD COLUMN IF NOT EXISTS account_id  uuid REFERENCES ledger_accounts(id);
ALTER TABLE holds ADD COLUMN IF NOT EXISTS reference   varchar(96);
ALTER TABLE holds ADD COLUMN IF NOT EXISTS currency    char(3);
ALTER TABLE holds ADD COLUMN IF NOT EXISTS hold_type   varchar(32);
ALTER TABLE holds ADD COLUMN IF NOT EXISTS source_type varchar(50);
ALTER TABLE holds ADD COLUMN IF NOT EXISTS source_id   uuid;
ALTER TABLE holds ADD COLUMN IF NOT EXISTS captured_at timestamptz;
ALTER TABLE holds ADD COLUMN IF NOT EXISTS released_at timestamptz;
ALTER TABLE holds ALTER COLUMN wallet_id DROP NOT NULL;

CREATE INDEX IF NOT EXISTS idx_holds_account ON holds (account_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_holds_source  ON holds (source_type, source_id);


ALTER TABLE ledger_transactions ADD COLUMN IF NOT EXISTS description text;
ALTER TABLE ledger_transactions ADD COLUMN IF NOT EXISTS posted_at   timestamptz NOT NULL DEFAULT now();

ALTER TABLE ledger_transactions ADD COLUMN IF NOT EXISTS reversal_of uuid REFERENCES ledger_transactions(id);

ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS account_id          uuid REFERENCES ledger_accounts(id);
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS direction           varchar(6) CHECK (direction IS NULL OR direction IN ('debit', 'credit'));
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS balance_after_minor bigint;
ALTER TABLE ledger_entries ADD COLUMN IF NOT EXISTS description         text;
ALTER TABLE ledger_entries ALTER COLUMN wallet_id  DROP NOT NULL;
ALTER TABLE ledger_entries ALTER COLUMN entry_type DROP NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ledger_entries_account
    ON ledger_entries (account_id, created_at DESC) WHERE account_id IS NOT NULL;

ALTER TABLE wallets ADD COLUMN IF NOT EXISTS account_id        uuid;
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS customer_id       uuid;
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS ledger_account_id uuid REFERENCES ledger_accounts(id);
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS name              text;
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS wallet_type       varchar(24) NOT NULL DEFAULT 'spending';
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS is_default        boolean NOT NULL DEFAULT true;

-- Existing wallets predate the customer/user split; user_id IS the customer.
UPDATE wallets SET customer_id = user_id WHERE customer_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_wallets_customer ON wallets (customer_id, currency);
