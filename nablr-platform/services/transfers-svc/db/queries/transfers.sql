-- noinspection SqlResolveForFile
-- noinspection SqlNoDataSourceInspectionForFile

-- name: CreateWallet :one
INSERT INTO wallets (user_id,currency) VALUES ($1,$2) ON CONFLICT (user_id,currency) DO UPDATE SET updated_at=now() RETURNING *;
-- name: WalletByUser :one
SELECT * FROM wallets WHERE user_id=$1 AND currency=$2;
-- name: WalletByUserForUpdate :one
SELECT * FROM wallets WHERE user_id=$1 AND currency=$2 FOR UPDATE;
-- name: WalletByIDForUpdate :one
SELECT * FROM wallets WHERE id=$1 FOR UPDATE;
-- name: CreateBeneficiary :one
INSERT INTO beneficiaries
(id, user_id, type, recipient_user_id, bank_code, bank_name, account_name,
 account_number_ciphertext, account_number_last4, account_number_blind_index,
 key_version, verification_status, verified_name, verification_provider,
currency, is_saved, cooling_period_ends_at,
 recipient_account_number, recipient_username, recipient_avatar_url, nickname)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21) RETURNING *;
-- name: BeneficiaryCiphertext :one
SELECT account_number_ciphertext FROM beneficiaries WHERE id=$1 AND status='active';
-- name: ListBeneficiaries :many
SELECT * FROM beneficiaries WHERE user_id=$1 AND status='active' AND is_saved ORDER BY is_favourite DESC,last_used_at DESC NULLS LAST,created_at DESC;
-- name: UpdateBeneficiaryPresentation :one
UPDATE beneficiaries SET
                         nickname=coalesce(sqlc.narg('nickname'),nickname),
                         is_favourite=coalesce(sqlc.narg('is_favourite')::boolean,is_favourite),
                         is_saved=coalesce(sqlc.narg('is_saved')::boolean,is_saved)
WHERE id=$1 AND user_id=$2 AND status='active' RETURNING *;
-- name: DeleteBeneficiary :execrows
UPDATE beneficiaries SET status='deleted' WHERE id=$1 AND user_id=$2 AND status='active';
-- name: MarkBeneficiaryUsed :exec
UPDATE beneficiaries SET last_used_at=now(),use_count=use_count+1 WHERE id=$1;
-- name: BeneficiaryByID :one
SELECT * FROM beneficiaries WHERE id=$1 AND user_id=$2 AND status='active';
-- name: BeneficiaryInternalByID :one
SELECT * FROM beneficiaries WHERE id=$1 AND status='active';
-- name: ActiveLimit :one

SELECT * FROM customer_limits WHERE user_id=$1 AND currency=$2 AND limit_type=$3 AND effective_to IS NULL;
-- name: UpsertTierLimit :exec
INSERT INTO customer_limits (user_id, limit_type, currency, amount_minor, source)
VALUES ($1, $2, $3, $4, 'tier')
    ON CONFLICT (user_id, limit_type, currency) WHERE effective_to IS NULL
    DO UPDATE SET amount_minor = EXCLUDED.amount_minor, effective_from = now()
       WHERE customer_limits.source = 'tier';
-- name: DeleteTierLimit :exec

DELETE FROM customer_limits WHERE user_id=$1 AND limit_type=$2 AND currency=$3 AND source='tier';
-- name: LimitByUser :one
SELECT
  coalesce((SELECT cl.amount_minor FROM customer_limits cl WHERE cl.user_id=$1 AND cl.currency=$2 AND cl.limit_type='per_transaction' AND cl.effective_to IS NULL ORDER BY cl.effective_from DESC LIMIT 1), 0)::bigint AS per_transaction_minor,
  coalesce((SELECT cl.amount_minor FROM customer_limits cl WHERE cl.user_id=$1 AND cl.currency=$2 AND cl.limit_type='daily_outbound' AND cl.effective_to IS NULL ORDER BY cl.effective_from DESC LIMIT 1), 0)::bigint AS daily_outbound_minor,
  coalesce((SELECT cl.amount_minor FROM customer_limits cl WHERE cl.user_id=$1 AND cl.currency=$2 AND cl.limit_type='balance_cap' AND cl.effective_to IS NULL ORDER BY cl.effective_from DESC LIMIT 1), 0)::bigint AS balance_cap_minor,
  coalesce((SELECT cl.amount_minor FROM customer_limits cl WHERE cl.user_id=$1 AND cl.currency=$2 AND cl.limit_type='weekly_outbound' AND cl.effective_to IS NULL ORDER BY cl.effective_from DESC LIMIT 1), 0)::bigint AS weekly_outbound_minor,
  coalesce((SELECT cl.amount_minor FROM customer_limits cl WHERE cl.user_id=$1 AND cl.currency=$2 AND cl.limit_type='monthly_outbound' AND cl.effective_to IS NULL ORDER BY cl.effective_from DESC LIMIT 1), 0)::bigint AS monthly_outbound_minor,
  coalesce((SELECT cl.count_limit FROM customer_limits cl WHERE cl.user_id=$1 AND cl.currency=$2 AND cl.limit_type='daily_count' AND cl.effective_to IS NULL ORDER BY cl.effective_from DESC LIMIT 1), 0)::int AS daily_count_limit;
-- name: OutboundToday :one
SELECT coalesce(sum(send_amount_minor),0)::bigint FROM transfers WHERE sender_user_id=$1 AND send_currency=$2 AND status IN ('pending','processing','completed') AND created_at >= date_trunc('day',now());
-- name: OutboundThisWeek :one

SELECT coalesce(sum(send_amount_minor),0)::bigint FROM transfers WHERE sender_user_id=$1 AND send_currency=$2 AND status IN ('pending','processing','completed') AND created_at >= date_trunc('week',now());
-- name: OutboundThisMonth :one
-- Committed outbound in the current calendar month, same window/status logic.
SELECT coalesce(sum(send_amount_minor),0)::bigint FROM transfers WHERE sender_user_id=$1 AND send_currency=$2 AND status IN ('pending','processing','completed') AND created_at >= date_trunc('month',now());
-- name: TransferCountToday :one
SELECT count(*)::bigint FROM transfers WHERE sender_user_id=$1 AND send_currency=$2 AND status IN ('pending','processing','completed') AND created_at >= date_trunc('day',now());
-- name: InsertLedgerTransaction :one
INSERT INTO ledger_transactions (reference,kind,idempotency_key) VALUES ($1,$2,$3) RETURNING *;
-- name: InsertLedgerEntry :one
INSERT INTO ledger_entries (transaction_id,wallet_id,entry_type,amount_minor,currency) VALUES ($1,$2,$3,$4,$5) RETURNING *;
-- name: CreateHold :one
INSERT INTO holds (wallet_id,amount_minor,expires_at) VALUES ($1,$2,$3) RETURNING *;
-- name: ReserveWallet :execrows
UPDATE ledger_accounts la SET reserved_minor=la.reserved_minor+sqlc.arg(amount_minor),updated_at=now() FROM wallets w WHERE w.id=sqlc.arg(wallet_id) AND w.status='active' AND la.id=w.ledger_account_id AND la.status='active' AND la.available_minor >= sqlc.arg(amount_minor);
-- name: ReleaseHold :execrows
UPDATE holds SET status='released',resolved_at=now() WHERE id=$1 AND status='active';
-- name: ReleaseWalletReservation :exec
-- The money becomes spendable again by dropping the reservation alone.
UPDATE ledger_accounts la SET reserved_minor=la.reserved_minor-sqlc.arg(amount_minor),updated_at=now() FROM wallets w WHERE w.id=sqlc.arg(wallet_id) AND la.id=w.ledger_account_id;
-- name: CaptureHold :execrows

UPDATE holds SET status='captured',resolved_at=now() WHERE id=$1 AND status='active';
-- name: ExtendHold :exec
UPDATE holds SET expires_at=$2 WHERE id=$1 AND status='active';
-- name: ConsumeWalletReservation :exec
UPDATE ledger_accounts la SET balance_minor=la.balance_minor-sqlc.arg(amount_minor),reserved_minor=la.reserved_minor-sqlc.arg(amount_minor),updated_at=now() FROM wallets w WHERE w.id=sqlc.arg(wallet_id) AND la.id=w.ledger_account_id;
-- name: CreditWallet :exec
-- Money in. This is the statement an inbound collection ultimately runs.
UPDATE ledger_accounts la SET balance_minor=la.balance_minor+sqlc.arg(amount_minor),updated_at=now() FROM wallets w WHERE w.id=sqlc.arg(wallet_id) AND w.status='active' AND la.id=w.ledger_account_id;
-- name: CreateTransfer :one
INSERT INTO transfers(reference,sender_user_id,source_wallet_id,beneficiary_id,destination_wallet_id,type,
                      send_amount_minor,send_currency,receive_amount_minor,receive_currency,total_debit_minor,status,hold_id,idempotency_key,narrative)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING *;
-- name: TransferByKey :one
SELECT * FROM transfers WHERE sender_user_id=$1 AND idempotency_key=$2;
-- name: TransferByID :one
SELECT * FROM transfers WHERE id=$1 AND sender_user_id=$2;
-- name: TransferInternalByID :one
SELECT * FROM transfers WHERE id=$1;
-- name: ListTransfers :many
SELECT * FROM transfers WHERE sender_user_id=$1 ORDER BY created_at DESC LIMIT $2;
-- name: CompleteTransfer :execrows
UPDATE transfers SET status='completed',completed_at=now() WHERE id=$1 AND status IN ('pending','processing','under_review');
-- name: FailTransfer :execrows
UPDATE transfers SET status='failed',failure_code=$2,failure_reason=$3 WHERE id=$1 AND status IN ('pending','processing','under_review');
-- name: ReviewTransfer :execrows
UPDATE transfers SET status='under_review',failure_reason=$2 WHERE id=$1 AND status IN ('pending','processing');
-- name: UnderReviewTransfersBefore :many
SELECT * FROM transfers WHERE status='under_review' AND created_at<$1 ORDER BY created_at LIMIT $2;
-- name: CreateQuote :one
INSERT INTO transfer_quotes(user_id,source_wallet_id,beneficiary_id,type,send_amount_minor,send_currency,receive_amount_minor,receive_currency,fee_minor,total_debit_minor,high_value,high_value_note,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING *;
-- name: ConsumeQuote :one
UPDATE transfer_quotes SET status='used' WHERE id=$1 AND user_id=$2 AND status='active' AND expires_at>now() RETURNING *;
-- name: QuoteByID :one
SELECT * FROM transfer_quotes WHERE id=$1 AND user_id=$2;
-- name: ListQuotes :many
-- The customer's quotes, newest first. Read-only like QuoteByID: listing never
-- consumes a quote. A limit keeps a pathological writer from flooding the
-- screen with hundreds of rows nobody will scroll through.
SELECT * FROM transfer_quotes WHERE user_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2;
-- name: CreateTransferEvent :one
INSERT INTO transfer_events(transfer_id,from_status,to_status,actor_type,actor_user_id,reason,provider_payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp()) RETURNING *;
-- name: TransferEventsByTransfer :many
SELECT id, from_status, to_status, actor_type, reason, created_at
FROM transfer_events WHERE transfer_id=$1 ORDER BY created_at ASC, id ASC;
-- name: RecentRecipients :many
SELECT b.id, b.type, b.account_name, b.verified_name, b.nickname,
       b.bank_name, b.bank_code, b.account_number_last4,
       b.recipient_account_number, b.recipient_username, b.recipient_avatar_url,
       max(t.created_at)::timestamptz AS last_sent_at,
       count(*)::bigint AS transfer_count
FROM transfers t
JOIN beneficiaries b ON b.id = t.beneficiary_id
WHERE t.sender_user_id = $1 AND b.status = 'active'
GROUP BY b.id
ORDER BY last_sent_at DESC
LIMIT $2;
-- name: TransitionTransfer :execrows
UPDATE transfers SET status=$2 WHERE id=$1 AND status=$3;
-- name: CancelTransfer :execrows
UPDATE transfers SET status='cancelled' WHERE id=$1 AND sender_user_id=$2 AND status IN ('pending','created');
-- name: TransferByProviderReference :one
SELECT * FROM transfers WHERE provider_name=$1 AND provider_reference=$2;
-- name: RecordProviderDispatch :exec
-- under_review is accepted so a parked payout can be attached to the reference
-- reconciliation eventually finds for it, moving it back into the normal
-- processing lifecycle instead of stranding it.
UPDATE transfers SET provider_name=$2,provider_reference=$3,submitted_at=now(),status='processing' WHERE id=$1 AND status IN ('pending','processing','under_review');
-- name: CreateProviderRequest :one
INSERT INTO provider_requests(transfer_id,provider_name,provider_reference,idempotency_key,outcome,error_code,error_message) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING *;
-- name: RecordDispatchIntent :one
INSERT INTO provider_requests(transfer_id,provider_name,idempotency_key,outcome)
VALUES($1,$2,$3,'intent')
    ON CONFLICT(provider_name,idempotency_key)
DO UPDATE SET outcome='intent' RETURNING *;
-- name: SettleDispatchIntent :exec
-- Closes an intent row with what the rail actually answered.
UPDATE provider_requests SET provider_reference=coalesce($3,provider_reference),outcome=$4,error_code=$5,error_message=$6
WHERE provider_name=$1 AND idempotency_key=$2;
-- name: DispatchIntentByTransfer :one
SELECT * FROM provider_requests WHERE transfer_id=$1 AND provider_name=$2 ORDER BY created_at DESC LIMIT 1;
-- name: RecordProviderWebhook :one
INSERT INTO provider_webhooks(provider_name,event_id,event_type,raw_body_sha256,payload,signature_valid,status) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(provider_name,event_id) DO NOTHING RETURNING *;
-- name: ExpiredActiveTransferHolds :many
SELECT h.*,t.id AS transfer_id FROM holds h JOIN transfers t ON t.hold_id=h.id WHERE h.status='active' AND h.expires_at<=now() AND t.status IN ('pending','processing') ORDER BY h.expires_at LIMIT $1 FOR UPDATE SKIP LOCKED;
-- name: PendingTransfersBefore :many
SELECT * FROM transfers WHERE status='pending' AND created_at<$1 AND provider_reference IS NULL ORDER BY created_at LIMIT $2;
-- name: ProcessingTransfersBefore :many
SELECT * FROM transfers WHERE status='processing' AND submitted_at<$1 AND provider_reference IS NOT NULL ORDER BY submitted_at LIMIT $2;
-- name: CreateOutboxEvent :one
INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload) VALUES($1,$2,$3,$4) RETURNING *;
-- name: ClaimOutboxEvents :many
WITH claimed AS (SELECT id FROM outbox_events WHERE status IN ('pending','failed') AND available_at<=now() ORDER BY available_at,id LIMIT $1 FOR UPDATE SKIP LOCKED) UPDATE outbox_events o SET status='processing',attempts=o.attempts+1 FROM claimed WHERE o.id=claimed.id RETURNING o.*;
-- name: MarkOutboxPublished :exec
UPDATE outbox_events SET status='published',published_at=now() WHERE id=$1;
-- name: MarkOutboxFailed :exec
UPDATE outbox_events SET status='failed',last_error=$2,available_at=$3 WHERE id=$1;
-- name: CreateSchedule :one
INSERT INTO scheduled_payments(
  user_id,source_wallet_id,beneficiary_id,payment_name,schedule_type,frequency,
  day_of_month,day_of_week,amount_minor,currency,narrative,next_run_at,end_date,max_occurrences
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING *;
-- name: ListSchedules :many
SELECT * FROM scheduled_payments WHERE user_id=$1 ORDER BY next_run_at;
-- name: CancelSchedule :execrows
UPDATE scheduled_payments SET status='cancelled',updated_at=now() WHERE id=$1 AND user_id=$2 AND status IN ('active','paused');
-- name: ClaimDueSchedules :many
SELECT * FROM scheduled_payments WHERE status='active' AND next_run_at<=now() ORDER BY next_run_at LIMIT $1 FOR UPDATE SKIP LOCKED;
-- name: RecordScheduleRun :one
INSERT INTO scheduled_payment_runs(scheduled_payment_id,scheduled_for,outcome,transfer_id,error_message) VALUES($1,$2,$3,$4,$5) ON CONFLICT(scheduled_payment_id,scheduled_for) DO NOTHING RETURNING *;
-- name: AdvanceSchedule :exec
UPDATE scheduled_payments SET next_run_at=$2,last_run_at=now(),occurrence_count=occurrence_count+1,consecutive_failures=0,status=$3,updated_at=now() WHERE id=$1;
-- name: FailScheduleRun :exec
UPDATE scheduled_payments SET next_run_at=$2,consecutive_failures=consecutive_failures+1,status=CASE WHEN consecutive_failures+1>=3 THEN 'paused' ELSE status END,pause_reason=$3,updated_at=now() WHERE id=$1;
-- name: CompleteSchedule :exec
UPDATE scheduled_payments SET status='completed',updated_at=now() WHERE id=$1 AND status='active';
-- name: ScheduleByID :one
SELECT * FROM scheduled_payments WHERE id=$1 AND user_id=$2;
-- name: ScheduleRunsBySchedule :many
SELECT * FROM scheduled_payment_runs WHERE scheduled_payment_id=$1 ORDER BY scheduled_for DESC LIMIT $2;
-- name: PauseSchedule :execrows
UPDATE scheduled_payments SET status='paused',pause_reason=$3,updated_at=now() WHERE id=$1 AND user_id=$2 AND status='active';
-- name: ResumeSchedule :execrows
UPDATE scheduled_payments SET status='active',pause_reason=NULL,next_run_at=GREATEST(next_run_at,now()),updated_at=now() WHERE id=$1 AND user_id=$2 AND status='paused';
-- name: UpdateSchedulePresentation :one
UPDATE scheduled_payments
   SET payment_name=$3, amount_minor=$4, narrative=$5, next_run_at=$6,
       frequency=$7, day_of_month=$8, day_of_week=$9, end_date=$10,
       max_occurrences=$11, updated_at=now()
 WHERE id=$1 AND user_id=$2 AND status='active' RETURNING *;

-- name: ListBanks :many
SELECT * FROM banks WHERE is_active ORDER BY non_interest DESC, name;
-- name: BankByCode :one
SELECT * FROM banks WHERE code=$1;
-- name: SuggestBanksCatalog :many
SELECT code, name, novac_code FROM banks WHERE is_active ORDER BY non_interest DESC, name;
-- name: BankTimingSamples :many
-- Duration (seconds) of completed payouts to a bank, newest first, bounded so a
-- busy rail cannot make the timing query grow without limit.
SELECT b.bank_code AS bank_code, b.bank_name AS bank_name,
       extract(epoch FROM (t.completed_at - coalesce(t.submitted_at, t.created_at)))::float8 AS duration_seconds
  FROM transfers t
  JOIN beneficiaries b ON b.id = t.beneficiary_id
 WHERE b.bank_code = $1
   AND b.type = 'bank'
   AND t.status = 'completed'
   AND t.completed_at IS NOT NULL
   AND t.created_at >= now() - interval '90 days'
 ORDER BY t.created_at DESC
 LIMIT $2;
-- name: BeneficiaryTransferHistory :many
-- Completed transfers to one payee over the last six months, oldest first — the
-- shape /cadence payload for /transfers/suggestions.
SELECT send_amount_minor, send_currency, completed_at
  FROM transfers
 WHERE sender_user_id = $1 AND beneficiary_id = $2 AND status = 'completed'
   AND completed_at IS NOT NULL
   AND created_at >= now() - interval '180 days'
 ORDER BY created_at ASC;

-- name: CreateReconciliationRun :one
INSERT INTO reconciliation_runs(provider_name,reconciliation_type,period_start,period_end,currency,status,triggered_by)
VALUES($1,$2,$3,$4,$5,'running',$6) RETURNING *;
-- name: CompleteReconciliationRun :exec
UPDATE reconciliation_runs
   SET status=$2, provider_item_count=$3, ledger_item_count=$4, matched_count=$5,
       break_count=$6, provider_total_minor=$7, ledger_total_minor=$8,
       variance_minor=$9, completed_at=now()
 WHERE id=$1;
-- name: InsertReconciliationItem :exec
INSERT INTO reconciliation_items(run_id,match_status,provider_reference,our_reference,
       provider_amount_minor,provider_status,provider_timestamp,provider_payload,
       ledger_transaction_id,ledger_amount_minor,currency,variance_minor)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12);
-- name: ListReconciliationRuns :many
SELECT * FROM reconciliation_runs ORDER BY created_at DESC LIMIT $1;
-- name: CompletedTransfersForReconciliation :many
SELECT id, reference, coalesce(provider_reference,'') AS provider_reference,
       send_amount_minor AS amount_minor, send_currency AS currency, status, created_at
  FROM transfers
 WHERE provider_name = $1
   AND status IN ('completed','failed')
   AND created_at BETWEEN $2 AND $3
 ORDER BY created_at ASC;

-- name: ProviderWebhookByID :one
SELECT * FROM provider_webhooks WHERE id=$1;
-- name: ListProviderWebhooks :many
SELECT * FROM provider_webhooks ORDER BY created_at DESC LIMIT $1;
-- name: MarkWebhookReplayed :exec
UPDATE provider_webhooks
   SET status='replayed', processing_attempts=processing_attempts+1,
       processed_at=now(), replayed_from_id=$2, replayed_by=$3
 WHERE id=$1;

-- name: RoundUpAccrualsDueForSweep :many
-- Accruals at or over their sweep threshold whose customer has a spend wallet
-- ledger account to debit; the sweep posts ONE ledger transaction per accrual.
SELECT a.id AS accrual_id, a.customer_id, a.currency, w.id AS wallet_id,
       w.ledger_account_id AS spend_account_id
  FROM round_up_accruals a
  JOIN wallets w ON w.user_id = a.customer_id AND w.currency = a.currency
 WHERE a.is_active AND a.accrued_minor >= a.sweep_threshold_minor
   AND w.ledger_account_id IS NOT NULL
 ORDER BY a.accrued_minor DESC
 LIMIT $1;
-- name: ClaimRoundUpAccrual :execrows
-- The claim: zero the accrual so a concurrent worker finds nothing to do; the
-- WHERE clause is what makes the race safe (only one winner per accrual).
UPDATE round_up_accruals
   SET accrued_minor = 0, last_swept_at = now(), updated_at = now()
 WHERE id = $1 AND is_active AND accrued_minor >= sweep_threshold_minor;
-- name: RoundUpUnSweptMinor :one
SELECT coalesce(sum(round_up_minor), 0)::bigint FROM round_up_items
 WHERE accrual_id = $1 AND swept_at IS NULL;
-- name: MarkRoundUpItemsSwept :exec
UPDATE round_up_items SET swept_at = now()
 WHERE accrual_id = $1 AND swept_at IS NULL;
-- name: RestoreRoundUpAccrual :exec
UPDATE round_up_accruals SET accrued_minor = accrued_minor + $2, updated_at = now() WHERE id = $1;

-- name: CreatePaymentRequest :one
INSERT INTO payment_requests
(id, requester_user_id, payer_user_id, amount_minor, currency, note,
 requester_name, requester_username, payer_name, payer_username, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING *;
-- name: PaymentRequestByID :one
SELECT * FROM payment_requests WHERE id=$1;
-- name: PaymentRequestsForUser :many
SELECT * FROM payment_requests
WHERE (sqlc.arg(incoming)::bool AND payer_user_id = sqlc.arg(user_id))
   OR (sqlc.arg(outgoing)::bool AND requester_user_id = sqlc.arg(user_id))
ORDER BY created_at DESC LIMIT sqlc.arg(lim);
-- name: MarkPaymentRequestPaid :one
UPDATE payment_requests SET status='paid', transfer_id=sqlc.arg(transfer_id), updated_at=now()
WHERE id=sqlc.arg(id) AND status='pending' RETURNING *;
-- name: SetPaymentRequestStatus :one
UPDATE payment_requests SET status=sqlc.arg(status), decline_reason=sqlc.narg(decline_reason), updated_at=now()
WHERE id=sqlc.arg(id) AND status='pending' RETURNING *;
-- name: InternalBeneficiaryForRecipient :one
SELECT * FROM beneficiaries
WHERE user_id=$1 AND recipient_user_id=$2 AND status='active'
ORDER BY is_saved DESC, created_at DESC LIMIT 1;

-- name: BankBeneficiaryForAccount :one
SELECT * FROM beneficiaries
WHERE user_id=$1 AND account_number_blind_index=$2 AND bank_code=$3 AND status='active'
ORDER BY is_saved DESC, created_at DESC LIMIT 1;
