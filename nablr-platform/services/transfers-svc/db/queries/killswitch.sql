-- Queries behind the payments kill switch and the balance proof that can trip it.
-- The kill switch is a single row in feature_flags; the balance proof reads the
-- ledger and reports the two invariants that, if broken, mean money is unaccounted
-- for: a ledger transaction that does not net to zero, and a customer account that
-- has gone negative without permission.

-- FeatureFlagEnabled reads one flag. NoRows (an absent row) is not an error: the
-- caller reads it as "flag unset" and applies the flag's documented default.
-- name: FeatureFlagEnabled :one
SELECT enabled FROM feature_flags WHERE key = $1;

-- SetFeatureFlag is the single writer of a flag: it upserts so flipping a switch
-- that was never set and re-flipping one that was are the same operation, and
-- stamps updated_at + the human-readable note recording why.
-- name: SetFeatureFlag :exec
INSERT INTO feature_flags (key, enabled, note, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (key) DO UPDATE
  SET enabled = EXCLUDED.enabled, note = EXCLUDED.note, updated_at = now();

-- UnbalancedLedgerTransactions lists any ledger transaction whose entries do not
-- net to zero within a currency. Double-entry means every transaction — posted or
-- reversed — must balance on its own, so this is checked across all of them, not
-- just posted ones. A non-empty result is a hard integrity break. LIMIT bounds the
-- output: existence and a few examples are all the proof needs.
-- name: UnbalancedLedgerTransactions :many
SELECT le.transaction_id,
       le.currency,
       sum(CASE WHEN le.entry_type = 'credit' THEN le.amount_minor ELSE -le.amount_minor END)::bigint AS net_minor
FROM ledger_entries le
GROUP BY le.transaction_id, le.currency
HAVING sum(CASE WHEN le.entry_type = 'credit' THEN le.amount_minor ELSE -le.amount_minor END) <> 0
LIMIT $1;

-- NegativeLedgerAccounts lists customer accounts that have gone below zero without
-- being permitted to (allows_negative marks the few system accounts, e.g. a
-- settlement clearing account, that legitimately carry a negative balance). A
-- non-empty result means an account was debited past its funds.
-- name: NegativeLedgerAccounts :many
SELECT id, account_type, currency, balance_minor
FROM ledger_accounts
WHERE balance_minor < 0 AND allows_negative = false
LIMIT $1;

-- CountLedgerTransactions reports how many ledger transactions the proof scanned,
-- so a clean proof records the size of what it verified rather than just "no
-- breaks found".
-- name: CountLedgerTransactions :one
SELECT count(*)::bigint FROM ledger_transactions;
