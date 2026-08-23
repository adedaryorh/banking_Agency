
CREATE TABLE IF NOT EXISTS banks (
    code         text PRIMARY KEY,
    name         text NOT NULL,
    non_interest boolean NOT NULL DEFAULT false,
    is_active    boolean NOT NULL DEFAULT true,
    -- novac_code is the rail's code for this bank when it differs from ours
    -- (message 0037 mapped the same way; NULL means "use our code").
    novac_code   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

INSERT INTO banks (code, name, non_interest) VALUES
    ('301', 'Jaiz Bank',            true),
    ('302', 'TAJ Bank',             true),
    ('303', 'Lotus Bank',           true),
    ('044', 'Access Bank',          false),
    ('023', 'Citibank',             false),
    ('050', 'Ecobank',              false),
    ('070', 'Fidelity Bank',        false),
    ('011', 'First Bank',           false),
    ('214', 'FCMB',                 false),
    ('058', 'GTBank',               false),
    ('030', 'Heritage Bank',        false),
    ('082', 'Keystone Bank',        false),
    ('076', 'Polaris Bank',         false),
    ('221', 'Stanbic IBTC Bank',    false),
    ('068', 'Standard Chartered',   false),
    ('232', 'Sterling Bank',        false),
    ('032', 'Union Bank',           false),
    ('033', 'UBA',                  false),
    ('215', 'Unity Bank',           false),
    ('035', 'Wema Bank',            false),
    ('057', 'Zenith Bank',          false),
    -- Digital banks and MFBs on the NIP network.
    ('999992', 'OPay',              false),
    ('999991', 'PalmPay',           false),
    ('50211',  'Kuda',              false),
    ('50515',  'Moniepoint',        false),
    ('51318',  'FairMoney',         false),
    ('565',    'Carbon',            false),
    ('566',    'VFD MFB',           false),
    ('51310',  'Sparkle',           false),
    ('090267', 'Kuda MFB',          false),
    ('50739',  'Mkobo MFB',         false),
    ('103',    'Globus Bank',       false),
    ('104',    'Parallex Bank',     false),
    ('105',    'Premium Trust Bank', false),
    ('106',    'Signature Bank',    false),
    ('107',    'Optimus Bank',      false),
    ('101',    'Providus Bank',     false),
    ('100',    'SunTrust Bank',     false),
    ('102',    'Titan Trust Bank',  false)
ON CONFLICT (code) DO NOTHING;

-- A bank without a Novac mapping is sent to the rail under our own code;
-- NULL and explicit self-mapping behave identically at dispatch time.
UPDATE banks SET novac_code = code WHERE novac_code IS NULL;

-- ---------------------------------------------------------------------------
-- Statement reconciliation (migration 0013_providers_operations).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS reconciliation_runs (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_name       varchar(64) NOT NULL,
    reconciliation_type varchar(24) NOT NULL DEFAULT 'payout',
    period_start        timestamptz NOT NULL,
    period_end          timestamptz NOT NULL,
    currency            char(3) NOT NULL DEFAULT 'NGN',
    status              varchar(24) NOT NULL DEFAULT 'running' CHECK (status IN (
                            'running', 'completed', 'completed_with_breaks',
                            'failed', 'cancelled'
                        )),
    provider_item_count int NOT NULL DEFAULT 0,
    ledger_item_count   int NOT NULL DEFAULT 0,
    matched_count       int NOT NULL DEFAULT 0,
    break_count         int NOT NULL DEFAULT 0,
    provider_total_minor bigint NOT NULL DEFAULT 0,
    ledger_total_minor  bigint NOT NULL DEFAULT 0,
    variance_minor      bigint NOT NULL DEFAULT 0,
    triggered_by        uuid,
    created_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz
);

CREATE TABLE IF NOT EXISTS reconciliation_items (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id                uuid NOT NULL REFERENCES reconciliation_runs(id) ON DELETE CASCADE,
    match_status          varchar(32) NOT NULL,
    provider_reference    varchar(160),
    our_reference         varchar(96),
    provider_amount_minor bigint,
    provider_status       varchar(32),
    provider_timestamp    timestamptz,
    provider_payload      jsonb,
    ledger_transaction_id uuid,
    ledger_amount_minor   bigint,
    currency              char(3),
    variance_minor        bigint NOT NULL DEFAULT 0,
    resolution_status     varchar(24),
    resolution_note       text,
    created_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_reconciliation_items_run ON reconciliation_items (run_id);
CREATE INDEX IF NOT EXISTS idx_reconciliation_runs_created ON reconciliation_runs (created_at DESC);

-- ---------------------------------------------------------------------------
-- Round-up sweep columns (the tables were created by 004_cards).
-- ---------------------------------------------------------------------------
ALTER TABLE round_up_accruals ADD COLUMN IF NOT EXISTS sweep_threshold_minor bigint NOT NULL DEFAULT 500 CHECK (sweep_threshold_minor > 0);
ALTER TABLE round_up_accruals ADD COLUMN IF NOT EXISTS last_swept_at       timestamptz;
ALTER TABLE round_up_items ADD COLUMN IF NOT EXISTS swept_at               timestamptz;

-- ---------------------------------------------------------------------------
-- provider_webhooks lifecycle: the receive path records 'received'; the admin
-- replay route terminally records 'replayed' (idempotent by provider ref in
-- the funding/transfer settle path, so a replayed callback moves no money
-- twice). 'ignored' marks callbacks dismissed after review.
-- ---------------------------------------------------------------------------
ALTER TABLE provider_webhooks DROP CONSTRAINT IF EXISTS provider_webhooks_status_check;
ALTER TABLE provider_webhooks ADD CONSTRAINT provider_webhooks_status_check
    CHECK (status IN ('received', 'processed', 'failed', 'replayed', 'ignored'));
ALTER TABLE provider_webhooks ADD COLUMN IF NOT EXISTS processing_attempts smallint NOT NULL DEFAULT 0;
ALTER TABLE provider_webhooks ADD COLUMN IF NOT EXISTS error_message       text;
ALTER TABLE provider_webhooks ADD COLUMN IF NOT EXISTS processed_at        timestamptz;
ALTER TABLE provider_webhooks ADD COLUMN IF NOT EXISTS replayed_from_id    uuid REFERENCES provider_webhooks(id);
ALTER TABLE provider_webhooks ADD COLUMN IF NOT EXISTS replayed_by         uuid;