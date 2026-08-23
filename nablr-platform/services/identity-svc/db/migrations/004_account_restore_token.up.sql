-- Allow "account_restore" as a verification_tokens.type value. Issued by
-- Login when a deactivated account's password check succeeds, so the app can
-- redeem it against the new unauthenticated restore endpoint without ever
-- getting a full access token for a still-deactivated account.
ALTER TABLE verification_tokens DROP CONSTRAINT IF EXISTS verification_tokens_type_check;
ALTER TABLE verification_tokens ADD CONSTRAINT verification_tokens_type_check
    CHECK (type IN ('email_verification', 'password_reset', 'magic_login', 'phone_onboarding', 'account_restore'));
