-- name: UserByUsername :one
SELECT * FROM users WHERE lower(nablr_username)=lower($1) AND deleted_at IS NULL;
-- name: ExpireStaleWaitlistByUsername :exec
UPDATE waitlist_entries SET status='expired', updated_at=now()
WHERE lower(nablr_username)=lower($1) AND status IN ('held','confirmed')
  AND ((status='held' AND hold_expires_at<=$2) OR (status='confirmed' AND expires_at<=$2));
-- name: ActiveWaitlistEntryByUsername :one
SELECT * FROM waitlist_entries WHERE lower(nablr_username)=lower($1) AND status IN ('held','confirmed');
-- name: CreateWaitlistEntry :one
INSERT INTO waitlist_entries (id,nablr_username,reservation_token_hash,status,held_at,hold_expires_at,requested_ip,user_agent,created_at,updated_at)
VALUES ($1,$2,$3,'held',$4,$5,$6,$7,$4,$4) RETURNING *;
-- name: WaitlistEntryByReservationTokenHash :one
SELECT * FROM waitlist_entries WHERE reservation_token_hash=$1;
-- name: ExpireWaitlistEntry :exec
UPDATE waitlist_entries SET status='expired', updated_at=$2 WHERE id=$1;
-- name: ConfirmWaitlistEntry :one
UPDATE waitlist_entries SET email=$2, status='confirmed', confirmed_at=$3, expires_at=$4, updated_at=$3
WHERE id=$1 AND status='held' RETURNING *;
-- name: ConfirmedWaitlistEntryByEmail :one
SELECT * FROM waitlist_entries WHERE lower(email)=lower($1) AND status='confirmed';
