-- name: CreatePINAuthorization :one
-- Mints the server-side proof that the payer's PIN was verified on the
-- authorize-pin step, bound to the exact amount, currency and recipient the
-- confirmation screen showed. RETURNING * so the handler can hand the id and
-- expiry back to the client to carry into POST /transfers.
INSERT INTO transfer_pin_authorizations (user_id, amount_minor, currency, recipient_descriptor, expires_at)
VALUES ($1,$2,$3,$4,$5) RETURNING *;

-- name: ConsumePINAuthorization :one
-- Single-use AND bound. Matches only an unconsumed, unexpired token that belongs
-- to this user and was minted for exactly this amount, currency and recipient.
-- `SET consumed_at = now() ... WHERE consumed_at IS NULL` is the atomic single-shot
-- guard — a second attempt (a replay, or two taps of Send) matches no row — and
-- binding amount+currency+recipient_descriptor is what stops a token issued for
-- one payment being spent on a different one. Run inside the money transaction so
-- the consume and the transfer commit or roll back together. pgx.ErrNoRows means
-- "no such live token for this exact payment", which the service reads as refuse.
UPDATE transfer_pin_authorizations
   SET consumed_at = now()
 WHERE id = $1 AND user_id = $2 AND amount_minor = $3 AND currency = $4
   AND recipient_descriptor = $5 AND consumed_at IS NULL AND expires_at > now()
 RETURNING *;
