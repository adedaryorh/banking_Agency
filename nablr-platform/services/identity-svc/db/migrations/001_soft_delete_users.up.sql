-- Soft-delete support for users.
-- Prior to this migration the users table was created without deleted_at, so
-- every lookup query that selects it failed with SQLSTATE 42703. This also
-- rebuilds the unique indexes as partial indexes that exclude soft-deleted
-- rows, allowing a user to re-register with the same email/phone/username
-- after deactivation.

ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
DROP INDEX IF EXISTS idx_users_email;
DROP INDEX IF EXISTS idx_users_phone_number;
DROP INDEX IF EXISTS idx_users_nablr_username;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users(lower(email)) WHERE deleted_at IS NULL AND email IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_phone_number ON users(phone_number) WHERE deleted_at IS NULL AND phone_number IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_nablr_username ON users(lower(nablr_username)) WHERE deleted_at IS NULL AND nablr_username IS NOT NULL;
