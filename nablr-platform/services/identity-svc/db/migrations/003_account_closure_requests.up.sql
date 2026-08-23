-- Account closure requests: reason capture, facial re-verification and a
-- short-lived confirmation code shown to the user before an account is
-- deactivated. Kept as its own audit-trail table (same shape as
-- kyc_verification_attempts) rather than columns on users, so the reason and
-- confirmation history survive independently of the account's live status.

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

CREATE INDEX IF NOT EXISTS idx_account_closure_requests_user_id
    ON account_closure_requests(user_id, created_at DESC);
