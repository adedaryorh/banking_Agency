ALTER TABLE scheduled_payments
  ADD COLUMN IF NOT EXISTS payment_name varchar(120) NOT NULL DEFAULT 'Scheduled payment',
  ADD COLUMN IF NOT EXISTS day_of_month smallint,
  ADD COLUMN IF NOT EXISTS day_of_week smallint;

ALTER TABLE scheduled_payments
  DROP CONSTRAINT IF EXISTS scheduled_payments_day_of_month_check,
  DROP CONSTRAINT IF EXISTS scheduled_payments_day_of_week_check;

ALTER TABLE scheduled_payments
  ADD CONSTRAINT scheduled_payments_day_of_month_check
    CHECK (day_of_month BETWEEN 1 AND 31),
  ADD CONSTRAINT scheduled_payments_day_of_week_check
    CHECK (day_of_week BETWEEN 0 AND 6);

UPDATE scheduled_payments
   SET payment_name = COALESCE(NULLIF(BTRIM(narrative), ''), 'Scheduled payment')
 WHERE payment_name = 'Scheduled payment';
