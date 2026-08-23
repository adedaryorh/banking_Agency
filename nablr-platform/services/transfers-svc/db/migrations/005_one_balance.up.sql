
ALTER TABLE ledger_accounts
    ADD COLUMN IF NOT EXISTS allows_negative boolean NOT NULL DEFAULT false;

UPDATE ledger_accounts SET allows_negative = true WHERE owner_type <> 'customer';


ALTER TABLE ledger_accounts DROP COLUMN IF EXISTS available_minor;
ALTER TABLE ledger_accounts
    ADD COLUMN available_minor bigint
    GENERATED ALWAYS AS (balance_minor - reserved_minor) STORED;

DO $$ BEGIN
    ALTER TABLE ledger_accounts
        ADD CONSTRAINT ledger_accounts_reserved_non_negative CHECK (reserved_minor >= 0);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE ledger_accounts
        ADD CONSTRAINT ledger_accounts_no_overdraft
        CHECK (allows_negative OR (balance_minor - reserved_minor) >= 0);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;


INSERT INTO ledger_accounts
    (id, account_type, currency, owner_type, customer_id, name, code,
     balance_minor, reserved_minor, pending_minor, status)
SELECT gen_random_uuid(), 'customer_asset', w.currency, 'customer', w.user_id,
       'wallet ' || w.currency,
       'customer_asset:' || w.currency || ':' || w.user_id::text,
       w.available_minor + w.reserved_minor, w.reserved_minor, 0,
       CASE WHEN w.status = 'active' THEN 'active' ELSE 'frozen' END
  FROM wallets w
 WHERE w.ledger_account_id IS NULL
ON CONFLICT (code) DO NOTHING;

UPDATE wallets w
   SET ledger_account_id = la.id
  FROM ledger_accounts la
 WHERE w.ledger_account_id IS NULL
   AND la.code = 'customer_asset:' || w.currency || ':' || w.user_id::text;

ALTER TABLE wallets ALTER COLUMN ledger_account_id SET NOT NULL;


CREATE OR REPLACE FUNCTION wallets_ensure_ledger_account() RETURNS trigger AS $$
DECLARE
    existing uuid;
    opened   uuid;
BEGIN
    IF NEW.ledger_account_id IS NOT NULL THEN
        RETURN NEW;
    END IF;

    SELECT id INTO existing FROM ledger_accounts
     WHERE code = 'customer_asset:' || NEW.currency || ':' || NEW.user_id::text;

    IF existing IS NOT NULL THEN
        NEW.ledger_account_id := existing;
        RETURN NEW;
    END IF;

    INSERT INTO ledger_accounts
        (account_type, currency, owner_type, customer_id, name, code,
         balance_minor, reserved_minor, pending_minor, status)
    VALUES ('customer_asset', NEW.currency, 'customer', NEW.user_id,
            'wallet ' || NEW.currency,
            'customer_asset:' || NEW.currency || ':' || NEW.user_id::text,
            0, 0, 0, 'active')
    RETURNING id INTO opened;

    NEW.ledger_account_id := opened;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_wallets_ensure_ledger_account ON wallets;
CREATE TRIGGER trg_wallets_ensure_ledger_account
    BEFORE INSERT ON wallets
    FOR EACH ROW EXECUTE FUNCTION wallets_ensure_ledger_account();


CREATE OR REPLACE FUNCTION ledger_accounts_mirror_to_wallet() RETURNS trigger AS $$
BEGIN
    IF NEW.balance_minor IS DISTINCT FROM OLD.balance_minor
       OR NEW.reserved_minor IS DISTINCT FROM OLD.reserved_minor THEN
        UPDATE wallets
           SET available_minor = NEW.available_minor,
               reserved_minor  = NEW.reserved_minor,
               updated_at      = now()
         WHERE ledger_account_id = NEW.id;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_ledger_accounts_mirror ON ledger_accounts;
CREATE TRIGGER trg_ledger_accounts_mirror
    AFTER UPDATE ON ledger_accounts
    FOR EACH ROW EXECUTE FUNCTION ledger_accounts_mirror_to_wallet();

COMMENT ON COLUMN wallets.available_minor IS
    'MIRROR of ledger_accounts.available_minor, maintained by trigger. Not authoritative and not written directly — see migration 005.';
COMMENT ON COLUMN wallets.reserved_minor IS
    'MIRROR of ledger_accounts.reserved_minor, maintained by trigger. Not authoritative and not written directly — see migration 005.';
