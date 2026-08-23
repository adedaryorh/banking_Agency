DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'transfers'
      AND column_name = 'customer_id'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'transfers'
      AND column_name = 'sender_user_id'
  ) THEN
    EXECUTE 'ALTER TABLE transfers RENAME COLUMN customer_id TO sender_user_id';
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'transfers'
      AND column_name = 'transfer_type'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'transfers'
      AND column_name = 'type'
  ) THEN
    EXECUTE 'ALTER TABLE transfers RENAME COLUMN transfer_type TO type';
  END IF;

  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'scheduled_payments'
      AND column_name = 'customer_id'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'scheduled_payments'
      AND column_name = 'user_id'
  ) THEN
    EXECUTE 'ALTER TABLE scheduled_payments RENAME COLUMN customer_id TO user_id';
  END IF;
END $$;

DO $$
DECLARE
  constraint_name text;
BEGIN
  FOR constraint_name IN
    SELECT c.conname
    FROM pg_constraint c
    JOIN pg_class t ON t.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = t.relnamespace
    WHERE n.nspname = 'public'
      AND t.relname = 'transfers'
      AND c.contype = 'c'
      AND pg_get_constraintdef(c.oid) LIKE '%local_bank%'
  LOOP
    EXECUTE format('ALTER TABLE transfers DROP CONSTRAINT IF EXISTS %I', constraint_name);
  END LOOP;
END $$;

UPDATE transfers SET type = 'bank' WHERE type IN ('local_bank', 'international', 'remittance');

ALTER TABLE transfers DROP CONSTRAINT IF EXISTS transfers_type_check;

ALTER TABLE transfers
  ADD CONSTRAINT transfers_type_check
  CHECK (type IN ('internal', 'bank'))
  NOT VALID;

DROP INDEX IF EXISTS idx_transfers_customer;
DROP INDEX IF EXISTS idx_transfers_idempotency;
DROP INDEX IF EXISTS idx_scheduled_payments_customer;

ALTER TABLE transfers DROP CONSTRAINT IF EXISTS transfers_idempotency_key_key;

CREATE UNIQUE INDEX IF NOT EXISTS transfers_sender_idempotency_idx
  ON transfers(sender_user_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS transfers_sender_created_idx
  ON transfers(sender_user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS scheduled_payments_customer_idx
  ON scheduled_payments(user_id, status);
