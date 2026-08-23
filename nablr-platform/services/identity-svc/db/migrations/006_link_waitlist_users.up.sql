ALTER TABLE waitlist_entries
  ADD COLUMN IF NOT EXISTS claimed_user_id uuid REFERENCES users(id),
  ADD COLUMN IF NOT EXISTS claimed_at timestamptz;

CREATE UNIQUE INDEX IF NOT EXISTS idx_waitlist_claimed_user
  ON waitlist_entries(claimed_user_id)
  WHERE claimed_user_id IS NOT NULL;

WITH eligible AS (
  SELECT w.id AS waitlist_id, u.id AS user_id, w.nablr_username
  FROM waitlist_entries w
  JOIN users u ON lower(u.email) = lower(w.email)
  WHERE w.status = 'confirmed'
    AND w.expires_at > now()
    AND u.email_verified_at IS NOT NULL
    AND u.deleted_at IS NULL
    AND (u.nablr_username IS NULL OR lower(u.nablr_username) = lower(w.nablr_username))
    AND NOT EXISTS (
      SELECT 1 FROM users owner
      WHERE owner.deleted_at IS NULL
        AND owner.id <> u.id
        AND lower(owner.nablr_username) = lower(w.nablr_username)
    )
), assigned AS (
  UPDATE users u
  SET nablr_username = e.nablr_username, updated_at = now()
  FROM eligible e
  WHERE u.id = e.user_id
  RETURNING u.id
)
UPDATE waitlist_entries w
SET status = 'onboarded', claimed_user_id = e.user_id,
    claimed_at = now(), updated_at = now()
FROM eligible e
JOIN assigned a ON a.id = e.user_id
WHERE w.id = e.waitlist_id;
