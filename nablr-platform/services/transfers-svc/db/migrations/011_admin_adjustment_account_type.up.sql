ALTER TABLE ledger_accounts
  DROP CONSTRAINT IF EXISTS ledger_accounts_account_type_check;

ALTER TABLE ledger_accounts
  ADD CONSTRAINT ledger_accounts_account_type_check
  CHECK (account_type IN (
    'customer_asset',
    'provider_clearing',
    'fees',
    'revenue',
    'suspense',
    'settlement',
    'platform_adjustment'
  ));
