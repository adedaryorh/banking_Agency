CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email varchar(256) UNIQUE,
    phone_number varchar(20) UNIQUE,
    nablr_username varchar(50) UNIQUE,
    password_hash varchar(256),
    password_set boolean NOT NULL DEFAULT false,
    role varchar(20) NOT NULL DEFAULT 'buyer' CHECK (role IN ('creator', 'buyer', 'admin')),
    status varchar(30) NOT NULL DEFAULT 'pending_verification',
    phone_verified boolean NOT NULL DEFAULT false,
    phone_verified_at timestamptz,
    otp_code varchar(6),
    otp_expires_at timestamptz,
    nin_status varchar(30) NOT NULL DEFAULT 'not_started',
    nin_verified boolean NOT NULL DEFAULT false,
    facial_status varchar(30) NOT NULL DEFAULT 'not_started',
    facial_verified boolean NOT NULL DEFAULT false,
    email_verified_at timestamptz,
    last_login_at timestamptz,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE users ADD COLUMN IF NOT EXISTS nablr_username varchar(50);
ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_at timestamptz;

-- Partial unique indexes that exclude soft-deleted users (deleted_at IS NULL)
-- This allows users to re-register with the same email/phone/username after deactivation
DROP INDEX IF EXISTS idx_users_email;
DROP INDEX IF EXISTS idx_users_phone_number;
DROP INDEX IF EXISTS idx_users_nablr_username;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users(lower(email)) WHERE deleted_at IS NULL AND email IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_phone_number ON users(phone_number) WHERE deleted_at IS NULL AND phone_number IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_nablr_username ON users(lower(nablr_username)) WHERE deleted_at IS NULL AND nablr_username IS NOT NULL;

-- The 10-digit handle other Nablr customers pay this person by: their phone
-- number without the leading zero. NOT the funding account an external bank
-- credits — that lives in transfers-svc.
ALTER TABLE users ADD COLUMN IF NOT EXISTS account_number varchar(10);

-- Profile image a user uploads through their own profile-image endpoint. The
-- column stores the object-storage key; the binary object lives in the service's
-- object store (local disk or S3).
ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_url varchar(500);
DO $$ BEGIN
    ALTER TABLE users ADD CONSTRAINT users_account_number_shape
        CHECK (account_number IS NULL OR account_number ~ '^[0-9]{10}$');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- Unique among LIVE users only, matching the partial indexes this table
-- already uses for email, phone and username: a deactivated account must not
-- hold its number against someone re-registering with the same phone.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_account_number
    ON users (account_number)
    WHERE deleted_at IS NULL AND account_number IS NOT NULL;

CREATE OR REPLACE FUNCTION account_number_from_phone(phone text)
RETURNS varchar(10) AS $$
DECLARE
    cleaned text;
BEGIN
    IF phone IS NULL THEN
        RETURN NULL;
    END IF;
    -- Spaces, dashes and brackets are cosmetic; the digits are the number.
    cleaned := regexp_replace(phone, '[\s\-()]', '', 'g');

    IF cleaned ~ '^\+234[0-9]{10}$' THEN
        RETURN substr(cleaned, 5);
    END IF;
    IF cleaned ~ '^234[0-9]{10}$' THEN
        RETURN substr(cleaned, 4);
    END IF;
    IF cleaned ~ '^0[0-9]{10}$' THEN
        RETURN substr(cleaned, 2);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- The trigger is what makes this reliable: identity-svc creates users down
-- several paths, and a derivation written in Go would have to be added to
-- each of them and remembered in the next one. A customer with no account
-- number is a customer nobody can pay.
CREATE OR REPLACE FUNCTION users_derive_account_number() RETURNS trigger AS $$
DECLARE
    derived varchar(10);
BEGIN
    -- Never reassign: the number is how people address this person, and one
    -- that changes when they update their phone is one that sends somebody
    -- else's money to them.
    IF NEW.account_number IS NOT NULL THEN
        RETURN NEW;
    END IF;

    derived := account_number_from_phone(NEW.phone_number);
    IF derived IS NULL THEN
        RETURN NEW;
    END IF;

    IF EXISTS (
        SELECT 1 FROM users
         WHERE account_number = derived
           AND deleted_at IS NULL
           AND id IS DISTINCT FROM NEW.id
    ) THEN
        RETURN NEW;
    END IF;

    NEW.account_number := derived;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_users_derive_account_number ON users;
CREATE TRIGGER trg_users_derive_account_number
    BEFORE INSERT OR UPDATE OF phone_number ON users
    FOR EACH ROW EXECUTE FUNCTION users_derive_account_number();

-- Email/password registrations created before password_set was wired already
-- have a valid bcrypt hash and should be treated as having a password.
UPDATE users SET password_set = true WHERE password_hash IS NOT NULL AND password_set = false;

CREATE TABLE IF NOT EXISTS auth_identities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider varchar(50) NOT NULL,
    provider_user_id varchar(255) NOT NULL,
    provider_email varchar(256),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_user_id)
);

CREATE INDEX IF NOT EXISTS idx_auth_identities_user_id ON auth_identities(user_id);

CREATE TABLE IF NOT EXISTS refresh_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash varchar(128) NOT NULL UNIQUE,
    family_id uuid NOT NULL,
    user_agent varchar(1000),
    ip_address varchar(64),
    last_used_at timestamptz,
    revoked_at timestamptz,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_family_id ON refresh_tokens(family_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_active ON refresh_tokens(user_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS verification_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type varchar(30) NOT NULL CHECK (type IN ('email_verification', 'password_reset', 'magic_login', 'phone_onboarding', 'account_restore')),
    token_hash varchar(128) NOT NULL UNIQUE,
    requested_ip varchar(64),
    user_agent varchar(1000),
    used_at timestamptz,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_verification_tokens_user_type ON verification_tokens(user_id, type);

-- Keep existing development databases aligned when phone onboarding is added
-- after the table was originally created.
ALTER TABLE verification_tokens DROP CONSTRAINT IF EXISTS verification_tokens_type_check;
ALTER TABLE verification_tokens ADD CONSTRAINT verification_tokens_type_check
    CHECK (type IN ('email_verification', 'password_reset', 'magic_login', 'phone_onboarding', 'account_restore'));

CREATE TABLE IF NOT EXISTS creator_profiles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    username varchar(100) NOT NULL UNIQUE,
    display_name varchar(150),
    bio text,
    avatar_url varchar(500),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS creator_social_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id uuid NOT NULL REFERENCES creator_profiles(id) ON DELETE CASCADE,
    platform varchar(50) NOT NULL,
    url varchar(500) NOT NULL,
    position integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_creator_social_links_creator ON creator_social_links(creator_id, position);

CREATE TABLE IF NOT EXISTS kyc_profiles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    tier smallint NOT NULL DEFAULT 0 CHECK (tier BETWEEN 0 AND 3),
    first_name varchar(100), middle_name varchar(100), last_name varchar(100),
    date_of_birth date, gender varchar(20), phone_number varchar(20),
    phone_status varchar(20) NOT NULL DEFAULT 'unverified',
    phone_verified_at timestamptz,
    bvn_encrypted text, bvn_last4 varchar(4), bvn_hash varchar(64),
    bvn_status varchar(20) NOT NULL DEFAULT 'unverified',
    bvn_verified_at timestamptz, bvn_phone_verified_at timestamptz,
    bvn_provider_ref varchar(160),
    bvn_selfie_status varchar(20) NOT NULL DEFAULT 'unverified',
    bvn_liveness_score double precision NOT NULL DEFAULT 0,
    bvn_face_match_score double precision NOT NULL DEFAULT 0,
    bvn_selfie_verified_at timestamptz,
    nin_encrypted text, nin_last4 varchar(4), nin_hash varchar(64),
    nin_status varchar(20) NOT NULL DEFAULT 'unverified',
    nin_verified_at timestamptz,
    address_line1 varchar(255), address_line2 varchar(255), city varchar(100),
    state varchar(100), postal_code varchar(20), country varchar(2) NOT NULL DEFAULT 'NG',
    address_status varchar(20) NOT NULL DEFAULT 'unverified',
    sanctioned boolean NOT NULL DEFAULT false,
    pep boolean NOT NULL DEFAULT false,
    reject_reason varchar(500),
    provider_payload jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (phone_status IN ('unverified','pending','verified','rejected','manual_review')),
    CHECK (bvn_status IN ('unverified','pending','verified','rejected','manual_review')),
    CHECK (nin_status IN ('unverified','pending','verified','rejected','manual_review')),
    CHECK (address_status IN ('unverified','pending','verified','rejected','manual_review'))
);

CREATE INDEX IF NOT EXISTS idx_kyc_profiles_bvn_hash ON kyc_profiles(bvn_hash);
CREATE INDEX IF NOT EXISTS idx_kyc_profiles_nin_hash ON kyc_profiles(nin_hash);
CREATE INDEX IF NOT EXISTS idx_kyc_profiles_tier ON kyc_profiles(tier);

CREATE TABLE IF NOT EXISTS kyc_verification_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind varchar(20) NOT NULL,
    provider varchar(40) NOT NULL,
    status varchar(20) NOT NULL,
    provider_ref varchar(160),
    match_score integer NOT NULL DEFAULT 0,
    failure_reason varchar(500),
    response_body jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_kyc_attempts_user_created ON kyc_verification_attempts(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_kyc_attempts_kind ON kyc_verification_attempts(kind);

CREATE TABLE IF NOT EXISTS kyc_facial_captures (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    attempt_id uuid NOT NULL REFERENCES kyc_verification_attempts(id) ON DELETE CASCADE,
    verification_request_id varchar(160),
    selfie_image bytea NOT NULL,
    liveness_frame_images jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_kyc_facial_captures_user ON kyc_facial_captures(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS account_closure_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reason varchar(60) NOT NULL,
    reason_note varchar(500),
    status varchar(30) NOT NULL DEFAULT 'pending_verification'
        CHECK (status IN ('pending_verification', 'pending_confirmation', 'confirmed', 'cancelled')),
    facial_verified_at timestamptz,
    code_hash varchar(128),
    code_expires_at timestamptz,
    code_attempts integer NOT NULL DEFAULT 0,
    confirmed_at timestamptz,
    restored_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_account_closure_requests_user_id ON account_closure_requests(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS transaction_limits (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tier smallint NOT NULL CHECK (tier BETWEEN 0 AND 3),
    currency varchar(10) NOT NULL,
    single_transaction_max_minor bigint NOT NULL,
    daily_max_minor bigint NOT NULL,
    monthly_max_minor bigint NOT NULL,
    max_balance_minor bigint NOT NULL,
    daily_count_max integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tier, currency)
);

CREATE TABLE IF NOT EXISTS limit_usage (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    currency varchar(10) NOT NULL,
    window_type varchar(10) NOT NULL CHECK (window_type IN ('daily','monthly')),
    window_key varchar(20) NOT NULL,
    used_minor bigint NOT NULL DEFAULT 0 CHECK (used_minor >= 0),
    used_count integer NOT NULL DEFAULT 0 CHECK (used_count >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, currency, window_type, window_key)
);

CREATE TABLE IF NOT EXISTS transaction_pins (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    pin_hash varchar(256) NOT NULL,
    failed_attempts integer NOT NULL DEFAULT 0,
    locked_until timestamptz,
    last_used_at timestamptz,
    last_changed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS otp_codes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose varchar(40) NOT NULL,
    channel varchar(10) NOT NULL CHECK (channel IN ('sms','email')),
    destination varchar(160) NOT NULL,
    code_hash varchar(128) NOT NULL,
    challenge_token varchar(128) NOT NULL UNIQUE,
    failed_attempts integer NOT NULL DEFAULT 0,
    consumed_at timestamptz,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_otp_codes_user_purpose ON otp_codes(user_id, purpose, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_otp_codes_expires_at ON otp_codes(expires_at);

CREATE TABLE IF NOT EXISTS user_devices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fingerprint varchar(128) NOT NULL,
    name varchar(150), platform varchar(40), push_token varchar(500),
    app_version varchar(40), os_version varchar(80), device_model varchar(120),
    trusted boolean NOT NULL DEFAULT false,
    blocked boolean NOT NULL DEFAULT false,
    last_ip varchar(64),
    latitude double precision,
    longitude double precision,
    last_seen_at timestamptz, trusted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, fingerprint)
);

ALTER TABLE user_devices ADD COLUMN IF NOT EXISTS app_version varchar(40);
ALTER TABLE user_devices ADD COLUMN IF NOT EXISTS os_version varchar(80);
ALTER TABLE user_devices ADD COLUMN IF NOT EXISTS device_model varchar(120);
ALTER TABLE user_devices ADD COLUMN IF NOT EXISTS latitude double precision;
ALTER TABLE user_devices ADD COLUMN IF NOT EXISTS longitude double precision;

CREATE TABLE IF NOT EXISTS risk_assessments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    operation varchar(60) NOT NULL,
    decision varchar(20) NOT NULL CHECK (decision IN ('allow','challenge','block')),
    score integer NOT NULL CHECK (score BETWEEN 0 AND 100),
    triggered_rules varchar(500), amount_minor bigint NOT NULL DEFAULT 0,
    currency varchar(10), device_id uuid REFERENCES user_devices(id) ON DELETE SET NULL,
    ip_address varchar(64), reference varchar(120),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_risk_assessments_user_created ON risk_assessments(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS tier3_applications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    status varchar(30) NOT NULL DEFAULT 'not_started',
    nin_status varchar(20) NOT NULL DEFAULT 'missing',
    address_status varchar(20) NOT NULL DEFAULT 'missing',
    location_status varchar(20) NOT NULL DEFAULT 'missing',
    utility_document_status varchar(20) NOT NULL DEFAULT 'missing',
    address_text varchar(500), address_line1 varchar(255), address_line2 varchar(255),
    city varchar(100), state varchar(100), country_code varchar(2) NOT NULL DEFAULT 'NG', postal_code varchar(20),
    latitude double precision, longitude double precision, accuracy_meters double precision,
    location_provider varchar(30), location_mocked boolean NOT NULL DEFAULT false,
    location_captured_at timestamptz, location_consent_at timestamptz,
    reverse_geocoded_address varchar(500), distance_meters double precision,
    submitted_at timestamptz, approved_at timestamptz, reviewed_at timestamptz,
    reviewed_by uuid REFERENCES users(id) ON DELETE SET NULL,
    rejection_reason varchar(500),
    created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tier3_documents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    application_id uuid NOT NULL REFERENCES tier3_applications(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    document_type varchar(40) NOT NULL,
    original_filename varchar(255) NOT NULL,
    issue_date date NOT NULL,
    object_key varchar(500) NOT NULL,
    content_type varchar(100) NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 varchar(128) NOT NULL,
    etag varchar(128), upload_expires_at timestamptz, uploaded_at timestamptz,
    extracted_name varchar(200), extracted_address varchar(500),
    status varchar(20) NOT NULL DEFAULT 'submitted', rejection_reason varchar(500),
    created_at timestamptz NOT NULL DEFAULT now(), reviewed_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_tier3_documents_user ON tier3_documents(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS outbox_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    exchange varchar(80) NOT NULL DEFAULT '', routing_key varchar(160) NOT NULL DEFAULT '',
    event_type varchar(120) NOT NULL, aggregate_type varchar(60) NOT NULL,
    aggregate_id uuid NOT NULL, payload jsonb NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'pending', attempts integer NOT NULL DEFAULT 0,
    last_error text, available_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_outbox_dispatch ON outbox_events(status, available_at);

CREATE OR REPLACE FUNCTION emit_onboarding_completed_event()
RETURNS trigger AS $$
BEGIN
    IF NEW.password_set = true AND NEW.status = 'active'
       AND (OLD.password_set IS DISTINCT FROM true OR OLD.status IS DISTINCT FROM 'active') THEN
        INSERT INTO outbox_events (
            exchange, routing_key, event_type, aggregate_type, aggregate_id, payload
        ) VALUES (
            'nabla.events', 'identity.onboarding.completed',
            'identity.onboarding.completed', 'user', NEW.id,
            jsonb_build_object('user_id', NEW.id, 'currency', 'NGN')
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_users_onboarding_completed ON users;
CREATE TRIGGER trg_users_onboarding_completed
AFTER UPDATE OF password_set, status ON users
FOR EACH ROW EXECUTE FUNCTION emit_onboarding_completed_event();

CREATE TABLE IF NOT EXISTS waitlist_entries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    nablr_username varchar(50) NOT NULL,
    email varchar(256),
    reservation_token_hash varchar(128) NOT NULL UNIQUE,
    status varchar(20) NOT NULL DEFAULT 'held' CHECK (status IN ('held','confirmed','expired','onboarded')),
    held_at timestamptz NOT NULL DEFAULT now(),
    hold_expires_at timestamptz NOT NULL,
    confirmed_at timestamptz,
    expires_at timestamptz,
    claimed_user_id uuid REFERENCES users(id),
    claimed_at timestamptz,
    requested_ip varchar(64),
    user_agent varchar(1000),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE waitlist_entries
  ADD COLUMN IF NOT EXISTS claimed_user_id uuid REFERENCES users(id),
  ADD COLUMN IF NOT EXISTS claimed_at timestamptz;

-- A username may only have one active (held or confirmed) reservation at a
-- time; expired/onboarded rows fall out of the index so the name frees up.
CREATE UNIQUE INDEX IF NOT EXISTS idx_waitlist_username_active ON waitlist_entries(lower(nablr_username)) WHERE status IN ('held','confirmed');
CREATE UNIQUE INDEX IF NOT EXISTS idx_waitlist_email_active ON waitlist_entries(lower(email)) WHERE status='confirmed';
CREATE INDEX IF NOT EXISTS idx_waitlist_status_username ON waitlist_entries(status, lower(nablr_username));
CREATE UNIQUE INDEX IF NOT EXISTS idx_waitlist_claimed_user ON waitlist_entries(claimed_user_id) WHERE claimed_user_id IS NOT NULL;

WITH eligible AS (
  SELECT w.id AS waitlist_id, u.id AS user_id, w.nablr_username
  FROM waitlist_entries w
  JOIN users u ON lower(u.email) = lower(w.email)
  WHERE w.status = 'confirmed' AND w.expires_at > now()
    AND u.email_verified_at IS NOT NULL AND u.deleted_at IS NULL
    AND (u.nablr_username IS NULL OR lower(u.nablr_username) = lower(w.nablr_username))
    AND NOT EXISTS (
      SELECT 1 FROM users owner
      WHERE owner.deleted_at IS NULL AND owner.id <> u.id
        AND lower(owner.nablr_username) = lower(w.nablr_username)
    )
), assigned AS (
  UPDATE users u SET nablr_username=e.nablr_username, updated_at=now()
  FROM eligible e WHERE u.id=e.user_id RETURNING u.id
)
UPDATE waitlist_entries w
SET status='onboarded', claimed_user_id=e.user_id, claimed_at=now(), updated_at=now()
FROM eligible e JOIN assigned a ON a.id=e.user_id
WHERE w.id=e.waitlist_id;

CREATE TABLE IF NOT EXISTS audit_logs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_type varchar(20) NOT NULL,
    actor_id uuid, action varchar(120) NOT NULL,
    entity_type varchar(60) NOT NULL, entity_id uuid,
    before_state jsonb, after_state jsonb,
    request_id varchar(80), ip_address varchar(64), user_agent varchar(500),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_entity ON audit_logs(entity_type, entity_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor ON audit_logs(actor_type, actor_id, created_at DESC);

-- Keep updated_at correct even for callers that do not set it explicitly.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DO $$
DECLARE table_name text;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'users','auth_identities','creator_profiles','creator_social_links',
        'kyc_profiles','transaction_limits','limit_usage','transaction_pins',
        'user_devices','tier3_applications','outbox_events','waitlist_entries'
    ] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_updated_at ON %I', table_name, table_name);
        EXECUTE format('CREATE TRIGGER trg_%I_updated_at BEFORE UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION set_updated_at()', table_name, table_name);
    END LOOP;
END $$;
