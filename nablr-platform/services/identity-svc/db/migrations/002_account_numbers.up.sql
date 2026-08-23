
ALTER TABLE users ADD COLUMN IF NOT EXISTS account_number varchar(10);

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

-- The trigger is what makes this reliable.
--
-- identity-svc creates users down several paths — email registration, phone
-- registration, OTP onboarding, admin creation — and a derivation written in
-- Go would have to be added to each of them and remembered in the next one. A
-- customer with no account number is a customer nobody can pay, and that is
-- not a failure worth leaving to a code review.
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

UPDATE users u
   SET account_number = account_number_from_phone(u.phone_number)
 WHERE u.account_number IS NULL
   AND u.deleted_at IS NULL
   AND account_number_from_phone(u.phone_number) IS NOT NULL
   AND NOT EXISTS (
         SELECT 1 FROM users other
          WHERE other.id <> u.id
            AND other.deleted_at IS NULL
            AND other.account_number = account_number_from_phone(u.phone_number));

COMMENT ON COLUMN users.account_number IS
    'The 10-digit handle other Nablr customers pay this person by: their phone number without the leading zero. NOT the funding account an external bank credits — that lives in transfers-svc.';
