CREATE TABLE IF NOT EXISTS beneficiaries (
    id UUID PRIMARY KEY,
    customer_id UUID NOT NULL,
    beneficiary_type VARCHAR(50) NOT NULL CHECK (beneficiary_type IN ('internal_user', 'bank_account')),
    display_name TEXT NOT NULL,
    nickname TEXT,
    beneficiary_customer_id UUID,
    country_code CHAR(2),
    currency CHAR(3),
    bank_code VARCHAR(20),
    bank_name TEXT,
    account_name TEXT,
    account_number_ciphertext BYTEA,
    account_number_last4 VARCHAR(4),
    account_number_blind_index BYTEA,
    key_version SMALLINT,
    verification_status VARCHAR(50) DEFAULT 'unverified',
    verified_name TEXT,
    verification_provider VARCHAR(50),
    cooling_period_ends_at TIMESTAMPTZ,
    is_favourite BOOLEAN DEFAULT FALSE,
    is_saved BOOLEAN DEFAULT TRUE,
    status VARCHAR(20) DEFAULT 'active' CHECK (status IN ('active', 'blocked', 'deleted')),
    use_count INTEGER DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX uq_beneficiaries_internal ON beneficiaries (customer_id, beneficiary_customer_id)
    WHERE beneficiary_type = 'internal_user' AND status <> 'deleted';
CREATE UNIQUE INDEX uq_beneficiaries_account ON beneficiaries (customer_id, account_number_blind_index)
    WHERE beneficiary_type = 'bank_account' AND status <> 'deleted';

CREATE INDEX idx_beneficiaries_customer ON beneficiaries(customer_id) WHERE status <> 'deleted';
CREATE INDEX idx_beneficiaries_blind_index ON beneficiaries(account_number_blind_index) WHERE account_number_blind_index IS NOT NULL;

CREATE TABLE IF NOT EXISTS transfers (
    id UUID PRIMARY KEY,
    reference VARCHAR(100) UNIQUE NOT NULL,
    customer_id UUID NOT NULL,
    initiated_by_user_id UUID,
    source_wallet_id UUID NOT NULL,
    destination_wallet_id UUID,
    beneficiary_id UUID,
    transfer_type VARCHAR(50) NOT NULL CHECK (transfer_type IN ('internal', 'local_bank', 'international', 'remittance')),
    send_amount_minor BIGINT NOT NULL,
    send_currency CHAR(3) NOT NULL,
    receive_amount_minor BIGINT NOT NULL,
    receive_currency CHAR(3) NOT NULL,
    fee_minor BIGINT DEFAULT 0,
    fee_currency CHAR(3),
    total_debit_minor BIGINT NOT NULL,
    fx_quote_id UUID,
    status VARCHAR(50) NOT NULL DEFAULT 'created' CHECK (status IN ('created', 'pending', 'processing', 'under_review', 'completed', 'failed', 'reversed', 'refunded', 'cancelled')),
    failure_code VARCHAR(100),
    failure_reason TEXT,
    hold_id UUID,
    ledger_transaction_id UUID,
    reversal_transaction_id UUID,
    provider_name VARCHAR(100),
    provider_reference VARCHAR(200),
    narrative TEXT,
    idempotency_key VARCHAR(200),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    submitted_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    CONSTRAINT transfers_completion CHECK (
        (status = 'completed' AND ledger_transaction_id IS NOT NULL AND completed_at IS NOT NULL) OR
        status <> 'completed'
    ),
    CONSTRAINT fk_transfers_beneficiary FOREIGN KEY (beneficiary_id) REFERENCES beneficiaries(id)
);

CREATE INDEX idx_transfers_customer ON transfers(customer_id, created_at DESC);
CREATE INDEX idx_transfers_status ON transfers(status) WHERE status IN ('pending', 'processing');
CREATE INDEX idx_transfers_provider_ref ON transfers(provider_reference) WHERE provider_reference IS NOT NULL;
CREATE INDEX idx_transfers_idempotency ON transfers(idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_transfers_beneficiary ON transfers(beneficiary_id);

CREATE TABLE IF NOT EXISTS transfer_events (
    id SERIAL PRIMARY KEY,
    transfer_id UUID NOT NULL,
    from_status VARCHAR(50) NOT NULL,
    to_status VARCHAR(50) NOT NULL,
    actor_type VARCHAR(50) NOT NULL CHECK (actor_type IN ('user', 'system', 'provider')),
    actor_user_id UUID,
    reason TEXT,
    provider_payload JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    CONSTRAINT fk_transfer_events_transfer FOREIGN KEY (transfer_id) REFERENCES transfers(id)
);

CREATE INDEX idx_transfer_events_transfer ON transfer_events(transfer_id, created_at);

CREATE TABLE IF NOT EXISTS scheduled_payments (
    id UUID PRIMARY KEY,
    customer_id UUID NOT NULL,
    source_wallet_id UUID NOT NULL,
    beneficiary_id UUID,
    schedule_type VARCHAR(50) NOT NULL CHECK (schedule_type IN ('one_off', 'recurring')),
    frequency VARCHAR(50),
    amount_minor BIGINT NOT NULL,
    currency CHAR(3) NOT NULL,
    narrative TEXT,
    next_run_at TIMESTAMPTZ NOT NULL,
    last_run_at TIMESTAMPTZ,
    end_date TIMESTAMPTZ,
    max_occurrences INTEGER,
    occurrence_count INTEGER DEFAULT 0,
    status VARCHAR(50) DEFAULT 'active' CHECK (status IN ('active', 'paused', 'completed', 'cancelled')),
    consecutive_failures INTEGER DEFAULT 0,
    pause_reason TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    CONSTRAINT fk_scheduled_beneficiary FOREIGN KEY (beneficiary_id) REFERENCES beneficiaries(id)
);

CREATE INDEX idx_scheduled_payments_customer ON scheduled_payments(customer_id);
CREATE INDEX idx_scheduled_payments_next_run ON scheduled_payments(next_run_at) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS scheduled_payment_runs (
    id SERIAL PRIMARY KEY,
    scheduled_payment_id UUID NOT NULL,
    scheduled_for TIMESTAMPTZ NOT NULL,
    executed_at TIMESTAMPTZ DEFAULT NOW(),
    outcome VARCHAR(50) NOT NULL CHECK (outcome IN ('succeeded', 'failed', 'insufficient_funds')),
    transfer_id UUID,
    error_message TEXT,
    CONSTRAINT fk_schedule_runs_schedule FOREIGN KEY (scheduled_payment_id) REFERENCES scheduled_payments(id),
    CONSTRAINT fk_schedule_runs_transfer FOREIGN KEY (transfer_id) REFERENCES transfers(id),
    CONSTRAINT uq_schedule_runs UNIQUE (scheduled_payment_id, scheduled_for)
);

CREATE INDEX idx_schedule_runs_schedule ON scheduled_payment_runs(scheduled_payment_id, executed_at DESC);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    idempotency_key VARCHAR(200) PRIMARY KEY,
    endpoint VARCHAR(100) NOT NULL,
    request_hash BYTEA NOT NULL,
    status VARCHAR(50) NOT NULL,
    response_body JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_idempotency_expires ON idempotency_keys(expires_at);

CREATE TABLE IF NOT EXISTS customer_limits (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id UUID NOT NULL,
    currency CHAR(3) NOT NULL,
    limit_type VARCHAR(50) NOT NULL CHECK (limit_type IN ('per_transaction', 'daily_outbound', 'balance_cap')),
    amount_minor BIGINT NOT NULL,
    effective_from TIMESTAMPTZ DEFAULT NOW(),
    effective_to TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_customer_limits_lookup ON customer_limits(customer_id, currency, limit_type) WHERE effective_to IS NULL;

CREATE TABLE IF NOT EXISTS banks (
    id SERIAL PRIMARY KEY,
    code VARCHAR(20) UNIQUE NOT NULL,
    name TEXT NOT NULL,
    country_code CHAR(2) NOT NULL DEFAULT 'NG',
    non_interest BOOLEAN DEFAULT FALSE,
    is_active BOOLEAN DEFAULT TRUE,
    novac_code VARCHAR(20),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_banks_code ON banks(code) WHERE is_active;
CREATE INDEX idx_banks_active ON banks(is_active, name);

CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

CREATE TRIGGER update_beneficiaries_updated_at BEFORE UPDATE ON beneficiaries
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_transfers_updated_at BEFORE UPDATE ON transfers
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_scheduled_payments_updated_at BEFORE UPDATE ON scheduled_payments
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_banks_updated_at BEFORE UPDATE ON banks
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();