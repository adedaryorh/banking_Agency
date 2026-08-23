CREATE TABLE IF NOT EXISTS wallet_transactions (
    id                   UUID PRIMARY KEY,
    wallet_id            UUID,
    customer_id          UUID NOT NULL,
    ledger_transaction_id UUID,
    direction            VARCHAR(10) NOT NULL CHECK (direction IN ('in', 'out')),
    amount_minor         BIGINT NOT NULL CHECK (amount_minor > 0),
    currency             CHAR(3) NOT NULL,
    fee_minor            BIGINT NOT NULL DEFAULT 0,
    balance_after_minor  BIGINT,
    transaction_type     VARCHAR(50) NOT NULL,
    counterparty_name    TEXT,
    counterparty_type    VARCHAR(50),
    counterparty_id      UUID,
    description          TEXT,
    source_type          VARCHAR(50),
    source_id            UUID,
    occurred_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_wallet_tx_customer ON wallet_transactions(customer_id, currency, occurred_at DESC);

CREATE TABLE IF NOT EXISTS card_authorisations (
    id            UUID PRIMARY KEY,
    customer_id   UUID NOT NULL,
    card_id       UUID,
    currency      CHAR(3) NOT NULL,
    amount_minor  BIGINT NOT NULL,
    decision      VARCHAR(20) NOT NULL,
    status        VARCHAR(30) NOT NULL,
    authorised_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_card_auths_customer ON card_authorisations(customer_id, currency, authorised_at DESC);

CREATE TABLE IF NOT EXISTS compliance_cases (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    case_reference VARCHAR(120) UNIQUE NOT NULL,
    customer_id    UUID NOT NULL,
    case_type      VARCHAR(100) NOT NULL,
    priority       VARCHAR(20) NOT NULL,
    status         VARCHAR(20) NOT NULL,
    title          TEXT NOT NULL,
    summary        TEXT,
    source         VARCHAR(50) NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_compliance_cases_customer ON compliance_cases(customer_id, status);

CREATE TABLE IF NOT EXISTS outbox_events (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type VARCHAR(100) NOT NULL,
    aggregate_id   UUID NOT NULL,
    event_type     VARCHAR(100) NOT NULL,
    payload        BYTEA NOT NULL,
    request_id     VARCHAR(100),

    -- raw-layer lifecycle
    processed_at   TIMESTAMPTZ,

    -- sqlc-layer lifecycle
    status         VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempts       INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT,
    available_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at   TIMESTAMPTZ
);

CREATE INDEX idx_outbox_events_ready ON outbox_events(available_at, id)
    WHERE status IN ('pending', 'failed') AND processed_at IS NULL;