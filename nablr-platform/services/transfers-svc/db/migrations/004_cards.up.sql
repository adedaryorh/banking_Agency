CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;


CREATE OR REPLACE FUNCTION deny_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'rows in % are immutable', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;


CREATE TABLE IF NOT EXISTS merchant_categories (
                                                   mcc              text PRIMARY KEY CHECK (mcc ~ '^[0-9]{4}$'),
                                                   description      text NOT NULL,
                                                   spending_group   text NOT NULL,
                                                   ethical_category text CHECK (ethical_category IN (
                                                                                                     'gambling', 'alcohol', 'adult_content', 'speculative_trading',
                                                                                                     'interest_lending', 'tobacco', 'nightlife'
                                                       )),

                                                   confidence       text NOT NULL DEFAULT 'high'
                                                       CHECK (confidence IN ('high', 'medium', 'low')),
                                                   created_at       timestamptz NOT NULL DEFAULT now(),
                                                   updated_at       timestamptz NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_merchant_categories_updated ON merchant_categories;
CREATE TRIGGER trg_merchant_categories_updated BEFORE UPDATE ON merchant_categories
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


INSERT INTO merchant_categories (mcc, description, spending_group, ethical_category, confidence) VALUES
                                                                                                     ('7995', 'Betting, casino gaming chips, lottery tickets', 'entertainment', 'gambling',            'high'),
                                                                                                     ('7801', 'Government licensed online casinos',            'entertainment', 'gambling',            'high'),
                                                                                                     ('7802', 'Government licensed horse/dog racing',           'entertainment', 'gambling',            'high'),
                                                                                                     ('9754', 'Gambling: horse racing, dog racing, state lottery', 'entertainment', 'gambling',         'high'),
                                                                                                     ('5813', 'Drinking places, bars, taverns, nightclubs',     'dining',        'alcohol',             'high'),
                                                                                                     ('5921', 'Package stores, beer, wine, liquor',             'groceries',     'alcohol',             'high'),
                                                                                                     ('5122', 'Drugs, drug proprietaries, druggist sundries',   'health',        NULL,                  'low'),
                                                                                                     ('5993', 'Cigar stores and stands',                        'retail',        'tobacco',             'high'),
                                                                                                     ('5972', 'Stamp and coin stores',                          'retail',        NULL,                  'low'),
                                                                                                     ('7273', 'Dating and escort services',                     'services',      'adult_content',       'medium'),
                                                                                                     ('5967', 'Direct marketing: inbound teleservices',         'services',      'adult_content',       'low'),
                                                                                                     ('7841', 'Video entertainment rental',                     'entertainment', 'adult_content',       'low'),
                                                                                                     ('6051', 'Non-financial institutions: currency, crypto',   'financial',     'speculative_trading', 'medium'),
                                                                                                     ('6211', 'Security brokers and dealers',                   'financial',     'speculative_trading', 'medium'),
                                                                                                     ('6012', 'Financial institutions: merchandise and services','financial',    'interest_lending',    'low'),
                                                                                                     ('6010', 'Financial institutions: manual cash disbursements','financial',   'interest_lending',    'low'),
                                                                                                     ('5811', 'Caterers',                                       'dining',        NULL,                  'high'),
                                                                                                     ('5812', 'Eating places and restaurants',                  'dining',        NULL,                  'high'),
                                                                                                     ('5411', 'Grocery stores and supermarkets',                'groceries',     NULL,                  'high'),
                                                                                                     ('5541', 'Service stations',                               'transport',     NULL,                  'high')
ON CONFLICT (mcc) DO NOTHING;


CREATE TABLE IF NOT EXISTS cards (
                                     id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                     customer_id           uuid NOT NULL,
                                     wallet_id             uuid NOT NULL REFERENCES wallets(id) ON DELETE RESTRICT,
    -- For child cards: the guardian who controls the card.
                                     controlling_user_id   uuid,

                                     card_type             text NOT NULL CHECK (card_type IN ('physical', 'virtual', 'disposable')),
                                     card_purpose          text NOT NULL DEFAULT 'personal' CHECK (card_purpose IN (
                                                                                                                    'personal', 'child', 'expense', 'organisation', 'travel', 'online_only'
                                         )),
                                     brand                 text CHECK (brand IN ('visa', 'mastercard', 'verve', 'afrigo', 'other')),
                                     currency              char(3) NOT NULL,

    -- Issuer-side identifiers. The token is what we use for every operation.
                                     provider_name         text NOT NULL,
                                     provider_card_id      text NOT NULL,
                                     provider_token        text,

    -- The only card data we retain.
                                     last4                 text CHECK (last4 IS NULL OR last4 ~ '^[0-9]{4}$'),
                                     expiry_month          smallint CHECK (expiry_month BETWEEN 1 AND 12),
                                     expiry_year           smallint CHECK (expiry_year BETWEEN 2020 AND 2100),
                                     name_on_card          text,

                                     label                 text NOT NULL,
                                     status                text NOT NULL DEFAULT 'created' CHECK (status IN (
                                                                                                             'created', 'inactive', 'active', 'frozen', 'expired',
                                                                                                             'lost', 'stolen', 'damaged', 'terminated'
                                         )),
                                     status_reason         text,
                                     activated_at          timestamptz,
                                     frozen_at             timestamptz,
                                     terminated_at         timestamptz,

    -- Physical card fulfilment. A physical card is LIVE from the moment it is
    -- issued: the plastic is a second way to use a card that already works, so
    -- these columns say where the plastic is, not whether the card can be used.
                                     delivery_status       text CHECK (delivery_status IN (
                                                                                           'not_applicable', 'requested', 'printing', 'dispatched',
                                                                                           'in_transit', 'delivered', 'returned', 'lost_in_transit'
                                         )),
                                     delivery_tracking_ref text,
                                     delivery_estimated_at date,
                                     delivered_at          timestamptz,

    -- Disposable cards self-terminate after first use or at expiry.
                                     single_use            boolean NOT NULL DEFAULT false,
                                     auto_expire_at        timestamptz,

                                     replaced_card_id      uuid REFERENCES cards(id),

                                     created_at            timestamptz NOT NULL DEFAULT now(),
                                     updated_at            timestamptz NOT NULL DEFAULT now(),

                                     CONSTRAINT cards_physical_delivery CHECK (
                                         card_type <> 'physical' OR delivery_status IS NOT NULL
                                         ),
                                     CONSTRAINT cards_disposable_single_use CHECK (
                                         card_type <> 'disposable' OR single_use
                                         )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_cards_provider ON cards (provider_name, provider_card_id);
CREATE INDEX IF NOT EXISTS idx_cards_customer ON cards (customer_id, status);
CREATE INDEX IF NOT EXISTS idx_cards_wallet ON cards (wallet_id);
CREATE INDEX IF NOT EXISTS idx_cards_auto_expire ON cards (auto_expire_at)
    WHERE auto_expire_at IS NOT NULL AND status = 'active';

DROP TRIGGER IF EXISTS trg_cards_updated ON cards;
CREATE TRIGGER trg_cards_updated BEFORE UPDATE ON cards
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- The issuer's own id for this cardholder.
--
-- A real card issuer holds its own customer record, carrying the KYC it is
-- answerable for, and will not print a card without one. Creating a second
-- cardholder for the same human on their second card would split their
-- spending controls and their identity across two records the issuer thinks
-- are two people — so the id is kept the first time and reused after.
--
-- On its own table rather than on customers, because transfers-svc has no
-- customers table: identity-svc owns that.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS card_cardholders (
                                                customer_id            uuid NOT NULL,
                                                provider_name          text NOT NULL,
                                                provider_customer_id   text NOT NULL,
                                                created_at             timestamptz NOT NULL DEFAULT now(),
                                                updated_at             timestamptz NOT NULL DEFAULT now(),
                                                PRIMARY KEY (customer_id, provider_name)
);

COMMENT ON COLUMN card_cardholders.provider_customer_id IS
    'The card issuer''s id for this cardholder. Not a secret, and not an identity number: the numbers themselves are never stored.';

DROP TRIGGER IF EXISTS trg_card_cardholders_updated ON card_cardholders;
CREATE TRIGGER trg_card_cardholders_updated BEFORE UPDATE ON card_cardholders
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Card controls. Applied at authorisation time, before the issuer decision is
-- returned. Each control is independently auditable.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS card_controls (
                                             card_id                 uuid PRIMARY KEY REFERENCES cards(id) ON DELETE CASCADE,

                                             allow_online            boolean NOT NULL DEFAULT true,
                                             allow_contactless       boolean NOT NULL DEFAULT true,
                                             allow_atm               boolean NOT NULL DEFAULT true,
    -- International is off by default: conservative until the customer opts in.
                                             allow_international     boolean NOT NULL DEFAULT false,
                                             allow_magstripe         boolean NOT NULL DEFAULT false,

                                             per_transaction_limit_minor bigint CHECK (per_transaction_limit_minor IS NULL OR per_transaction_limit_minor > 0),
                                             daily_limit_minor       bigint CHECK (daily_limit_minor IS NULL OR daily_limit_minor > 0),
                                             monthly_limit_minor     bigint CHECK (monthly_limit_minor IS NULL OR monthly_limit_minor > 0),
                                             atm_daily_limit_minor   bigint CHECK (atm_daily_limit_minor IS NULL OR atm_daily_limit_minor > 0),
                                             daily_count_limit       int CHECK (daily_count_limit IS NULL OR daily_count_limit > 0),

                                             allowed_countries       text[],
                                             blocked_countries       text[],
    -- MCC-level allow/block, independent of ethical spending controls.
                                             blocked_mccs            text[] NOT NULL DEFAULT '{}',
                                             allowed_mccs            text[],

    -- Ethical controls are stored separately (spending_controls) and evaluated
    -- alongside these; this flag records whether they apply to this card.
                                             apply_ethical_controls  boolean NOT NULL DEFAULT true,

                                             updated_by              uuid,
                                             created_at              timestamptz NOT NULL DEFAULT now(),
                                             updated_at              timestamptz NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_card_controls_updated ON card_controls;
CREATE TRIGGER trg_card_controls_updated BEFORE UPDATE ON card_controls
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Voluntary ethical spending controls.
--
-- A control is customer-owned, never applied by default, never visible to
-- anyone else, and the outcome copy is factual. Every block records the MCC
-- and its confidence so a miscategorised merchant can be disputed.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS spending_controls (
                                                 id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                                 customer_id        uuid NOT NULL,
    -- Optional narrowing: one card, one wallet, or one family member.
                                                 scope              text NOT NULL DEFAULT 'customer' CHECK (scope IN (
                                                                                                                      'customer', 'wallet', 'card', 'family_member'
                                                     )),
                                                 scope_id           uuid,

                                                 category           text NOT NULL CHECK (category IN (
                                                                                                      'gambling', 'alcohol', 'adult_content', 'speculative_trading',
                                                                                                      'interest_lending', 'tobacco', 'nightlife', 'custom_mcc', 'custom_merchant'
                                                     )),
    -- 'block' declines, 'warn' allows and notifies, 'allow' is an explicit
    -- exception that overrides a broader block.
                                                 action             text NOT NULL DEFAULT 'block' CHECK (action IN ('block', 'warn', 'allow')),

                                                 custom_mccs        text[] NOT NULL DEFAULT '{}',
                                                 custom_merchant_patterns text[] NOT NULL DEFAULT '{}',

    -- Who created it. A guardian-set control on a dependent cannot be removed
    -- by the dependent.
                                                 set_by_user_id     uuid NOT NULL,
                                                 is_guardian_set    boolean NOT NULL DEFAULT false,

                                                 is_active          boolean NOT NULL DEFAULT true,
                                                 created_at         timestamptz NOT NULL DEFAULT now(),
                                                 updated_at         timestamptz NOT NULL DEFAULT now(),

                                                 CONSTRAINT spending_controls_scope_pairing CHECK (
                                                     (scope = 'customer' AND scope_id IS NULL) OR (scope <> 'customer' AND scope_id IS NOT NULL)
                                                     ),
                                                 CONSTRAINT spending_controls_custom_values CHECK (
                                                     category <> 'custom_mcc' OR array_length(custom_mccs, 1) > 0
                                                     )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_spending_controls_active
    ON spending_controls (customer_id, scope,
                          coalesce(scope_id, '00000000-0000-0000-0000-000000000000'::uuid),
                          category)
    WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_spending_controls_customer
    ON spending_controls (customer_id) WHERE is_active;

DROP TRIGGER IF EXISTS trg_spending_controls_updated ON spending_controls;
CREATE TRIGGER trg_spending_controls_updated BEFORE UPDATE ON spending_controls
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS spending_control_overrides (
                                                          id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                                          control_id         uuid NOT NULL REFERENCES spending_controls(id) ON DELETE CASCADE,
                                                          customer_id        uuid NOT NULL,
                                                          requested_by_user_id uuid NOT NULL,
    -- Guardian-set controls need the guardian's approval to override.
                                                          approved_by_user_id uuid,
                                                          reason             text,
    -- Either a blanket override for a window, or a single named merchant.
                                                          merchant_name      text,
                                                          merchant_mcc       text REFERENCES merchant_categories(mcc),
                                                          starts_at          timestamptz NOT NULL DEFAULT now(),
                                                          expires_at         timestamptz NOT NULL,
                                                          used_at            timestamptz,
                                                          cancelled_at       timestamptz,
                                                          created_at         timestamptz NOT NULL DEFAULT now(),
                                                          CONSTRAINT spending_override_window CHECK (expires_at > starts_at)
);

CREATE INDEX IF NOT EXISTS idx_spending_overrides_live
    ON spending_control_overrides (control_id, expires_at)
    WHERE cancelled_at IS NULL;

CREATE TABLE IF NOT EXISTS spending_control_events (
                                                       id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                                       customer_id          uuid NOT NULL,
                                                       control_id           uuid REFERENCES spending_controls(id) ON DELETE SET NULL,
                                                       card_authorisation_id uuid,
                                                       outcome              text NOT NULL CHECK (outcome IN ('blocked', 'warned', 'allowed_by_override')),
                                                       category             text NOT NULL,
                                                       merchant_name        text,
                                                       merchant_mcc         text,
                                                       mcc_confidence       text,
                                                       amount_minor         bigint,
                                                       currency             char(3),
                                                       override_id          uuid REFERENCES spending_control_overrides(id),
    -- Customer-raised dispute: "this merchant is miscategorised".
                                                       disputed_at          timestamptz,
                                                       dispute_ticket_id    uuid,
                                                       created_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_spending_control_events_customer
    ON spending_control_events (customer_id, created_at DESC);


CREATE TABLE IF NOT EXISTS card_authorisations (
                                                   id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                                   customer_id   uuid NOT NULL,
                                                   card_id       uuid,
                                                   currency      char(3) NOT NULL,
                                                   amount_minor  bigint NOT NULL,
                                                   decision      varchar(20) NOT NULL,
                                                   status        varchar(30) NOT NULL,
                                                   authorised_at timestamptz NOT NULL DEFAULT now(),
                                                   created_at    timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS wallet_id            uuid;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS provider_name        text;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS provider_auth_id     text;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS billing_amount_minor bigint;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS billing_currency     char(3);
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS merchant_name        text;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS merchant_id          text;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS merchant_mcc         text REFERENCES merchant_categories(mcc);
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS merchant_country     text;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS merchant_city        text;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS entry_mode           text;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS is_international     boolean NOT NULL DEFAULT false;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS decline_reason       text;
-- Which ethical control declined it, so the customer can be shown a factual,
-- non-judgemental explanation and dispute a miscategorised merchant.
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS triggered_control_id uuid;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS hold_id              uuid REFERENCES holds(id);
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS settled_amount_minor bigint NOT NULL DEFAULT 0;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS expires_at           timestamptz;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS settled_at           timestamptz;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS reversed_at          timestamptz;
ALTER TABLE card_authorisations ADD COLUMN IF NOT EXISTS updated_at           timestamptz NOT NULL DEFAULT now();

DO $$ BEGIN
    ALTER TABLE card_authorisations
        ADD CONSTRAINT card_auth_decision CHECK (decision IN ('approved', 'declined', 'partial'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE card_authorisations
        ADD CONSTRAINT card_auth_status CHECK (status IN (
                                                          'pending', 'settled', 'partially_settled', 'reversed', 'expired', 'declined'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE card_authorisations
        ADD CONSTRAINT card_auth_decline_reason_values CHECK (decline_reason IS NULL OR decline_reason IN (
                                                                                                           'insufficient_funds', 'card_frozen', 'card_inactive', 'card_expired',
                                                                                                           'limit_exceeded', 'control_blocked', 'ethical_control_blocked',
                                                                                                           'country_blocked', 'mcc_blocked', 'risk_declined',
                                                                                                           'parent_approval_required', 'compliance_restriction', 'issuer_declined'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE card_authorisations
        ADD CONSTRAINT card_auth_decline_reason CHECK (decision <> 'declined' OR decline_reason IS NOT NULL);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE card_authorisations
        ADD CONSTRAINT card_auth_settled_within_amount CHECK (settled_amount_minor <= amount_minor);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE card_authorisations
        ADD CONSTRAINT card_auth_entry_mode CHECK (entry_mode IS NULL OR entry_mode IN (
                                                                                        'chip', 'contactless', 'magstripe', 'ecommerce', 'manual', 'atm', 'recurring'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

UPDATE card_authorisations SET provider_name = 'unknown' WHERE provider_name IS NULL;
UPDATE card_authorisations SET provider_auth_id = 'legacy:' || id::text WHERE provider_auth_id IS NULL;
ALTER TABLE card_authorisations ALTER COLUMN provider_name SET NOT NULL;
ALTER TABLE card_authorisations ALTER COLUMN provider_auth_id SET NOT NULL;


CREATE UNIQUE INDEX IF NOT EXISTS uq_card_auth_provider
    ON card_authorisations (provider_name, provider_auth_id);
CREATE INDEX IF NOT EXISTS idx_card_auth_card ON card_authorisations (card_id, authorised_at DESC);
CREATE INDEX IF NOT EXISTS idx_card_auth_expiry ON card_authorisations (expires_at) WHERE status = 'pending';

DROP TRIGGER IF EXISTS trg_card_auth_updated ON card_authorisations;
CREATE TRIGGER trg_card_auth_updated BEFORE UPDATE ON card_authorisations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Settled card transactions (clearing). These post to the ledger.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS card_transactions (
                                                 id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                                 card_id              uuid NOT NULL REFERENCES cards(id) ON DELETE RESTRICT,
                                                 authorisation_id     uuid REFERENCES card_authorisations(id),
                                                 wallet_id            uuid NOT NULL,
                                                 customer_id          uuid NOT NULL,

                                                 provider_name        text NOT NULL,
                                                 provider_transaction_id text NOT NULL,

                                                 transaction_type     text NOT NULL CHECK (transaction_type IN (
                                                                                                                'purchase', 'refund', 'atm_withdrawal', 'fee', 'reversal',
                                                                                                                'chargeback', 'chargeback_reversal', 'adjustment'
                                                     )),
                                                 amount_minor         bigint NOT NULL CHECK (amount_minor > 0),
                                                 currency             char(3) NOT NULL,
                                                 billing_amount_minor bigint,
                                                 billing_currency     char(3),
                                                 fx_rate_numerator    bigint,
                                                 fx_rate_scale        smallint,
                                                 fee_minor            bigint NOT NULL DEFAULT 0 CHECK (fee_minor >= 0),

                                                 merchant_name        text NOT NULL,
                                                 merchant_mcc         text REFERENCES merchant_categories(mcc),
                                                 merchant_country     text,

                                                 ledger_transaction_id uuid REFERENCES ledger_transactions(id),
                                                 wallet_transaction_id uuid REFERENCES wallet_transactions(id),

                                                 settled_at           timestamptz NOT NULL,
                                                 created_at           timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_card_transactions_provider
    ON card_transactions (provider_name, provider_transaction_id);
CREATE INDEX IF NOT EXISTS idx_card_transactions_card ON card_transactions (card_id, settled_at DESC);
CREATE INDEX IF NOT EXISTS idx_card_transactions_customer ON card_transactions (customer_id, settled_at DESC);

-- ---------------------------------------------------------------------------
-- Card lifecycle events (freeze, replace, report lost, PIN change attempts).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS card_events (
                                           id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                           card_id       uuid NOT NULL REFERENCES cards(id) ON DELETE RESTRICT,
                                           event_type    text NOT NULL CHECK (event_type IN (
                                                                                             'created', 'activated', 'frozen', 'unfrozen', 'pin_set', 'pin_changed',
                                                                                             'pin_reset', 'controls_updated', 'reported_lost', 'reported_stolen',
                                                                                             'replaced', 'terminated', 'details_viewed', 'delivery_updated'
                                               )),
                                           actor_user_id uuid,
                                           actor_type    text NOT NULL DEFAULT 'user' CHECK (actor_type IN ('user', 'system', 'admin', 'provider')),
                                           reason        text,
    -- Never contains card data; only which fields changed.
                                           changed_fields text[] NOT NULL DEFAULT '{}',
                                           ip_address    inet,
                                           created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_card_events_card ON card_events (card_id, created_at DESC);

DROP TRIGGER IF EXISTS trg_card_events_immutable ON card_events;
CREATE TRIGGER trg_card_events_immutable
    BEFORE UPDATE OR DELETE ON card_events
    FOR EACH ROW EXECUTE FUNCTION deny_mutation();

-- ---------------------------------------------------------------------------
-- Physical card delivery: one open request per card, honest status ladder.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS card_delivery_requests (
                                                      id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                                      card_id       uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
                                                      customer_id   uuid NOT NULL,
                                                      address_line1 text NOT NULL,
                                                      city          text NOT NULL,
                                                      state         text NOT NULL,
                                                      phone         text NOT NULL,
                                                      status        text NOT NULL DEFAULT 'requested'
                                                          CHECK (status IN ('requested', 'printing', 'shipped', 'delivered')),
                                                      created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_card_delivery_open
    ON card_delivery_requests (card_id) WHERE status <> 'delivered';

-- ---------------------------------------------------------------------------
-- Round-up accrual on card spend.
--
-- Spare change on a card payment accrues here; a sweep job moves the real
-- money later, in one posting, so statements stay readable. Ported from
-- usenablr-1.0's savings module reduced to the two tables the cards
-- settlement path touches — the multiplier lives on the accrual rather than
-- on a savings_pots row, because transfers-svc has no savings pots.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS round_up_accruals (
                                                 id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                                 customer_id         uuid NOT NULL,
                                                 currency            char(3) NOT NULL,
                                                 round_up_multiplier smallint NOT NULL DEFAULT 1 CHECK (round_up_multiplier BETWEEN 1 AND 10),
                                                 accrued_minor       bigint NOT NULL DEFAULT 0 CHECK (accrued_minor >= 0),
                                                 is_active           boolean NOT NULL DEFAULT true,
                                                 created_at          timestamptz NOT NULL DEFAULT now(),
                                                 updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_round_up_accruals_active
    ON round_up_accruals (customer_id, currency) WHERE is_active;

DROP TRIGGER IF EXISTS trg_round_up_accruals_updated ON round_up_accruals;
CREATE TRIGGER trg_round_up_accruals_updated BEFORE UPDATE ON round_up_accruals
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS round_up_items (
                                              id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                              accrual_id            uuid NOT NULL REFERENCES round_up_accruals(id) ON DELETE CASCADE,
    -- UNIQUE: one feed item rounds up once, however many times a settlement
    -- webhook is delivered.
                                              wallet_transaction_id uuid NOT NULL UNIQUE,
                                              spend_amount_minor    bigint NOT NULL CHECK (spend_amount_minor > 0),
                                              round_up_minor        bigint NOT NULL CHECK (round_up_minor > 0),
                                              created_at            timestamptz NOT NULL DEFAULT now()
);