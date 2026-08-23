-- name: KYCProfileByUserID :one
SELECT * FROM kyc_profiles WHERE user_id=$1;
-- name: KYCProfileByBVNHash :one
SELECT * FROM kyc_profiles WHERE bvn_hash=$1;
-- name: KYCProfileByNINHash :one
SELECT * FROM kyc_profiles WHERE nin_hash=$1;
-- name: CreateKYCProfile :one
INSERT INTO kyc_profiles (id,user_id,tier,first_name,middle_name,last_name,date_of_birth,gender,phone_number,phone_status,phone_verified_at,bvn_encrypted,bvn_last4,bvn_hash,bvn_status,bvn_verified_at,bvn_phone_verified_at,bvn_provider_ref,bvn_selfie_status,bvn_liveness_score,bvn_face_match_score,bvn_selfie_verified_at,nin_encrypted,nin_last4,nin_hash,nin_status,nin_verified_at,address_line1,address_line2,city,state,postal_code,country,address_status,sanctioned,pep,reject_reason,provider_payload,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40) RETURNING *;
-- name: UpdateKYCProfile :one
UPDATE kyc_profiles SET tier=$2,first_name=$3,middle_name=$4,last_name=$5,date_of_birth=$6,gender=$7,phone_number=$8,phone_status=$9,phone_verified_at=$10,bvn_encrypted=$11,bvn_last4=$12,bvn_hash=$13,bvn_status=$14,bvn_verified_at=$15,bvn_phone_verified_at=$16,bvn_provider_ref=$17,bvn_selfie_status=$18,bvn_liveness_score=$19,bvn_face_match_score=$20,bvn_selfie_verified_at=$21,nin_encrypted=$22,nin_last4=$23,nin_hash=$24,nin_status=$25,nin_verified_at=$26,address_line1=$27,address_line2=$28,city=$29,state=$30,postal_code=$31,country=$32,address_status=$33,sanctioned=$34,pep=$35,reject_reason=$36,provider_payload=$37,updated_at=now() WHERE id=$1 RETURNING *;
-- name: CreateKYCAttempt :one
INSERT INTO kyc_verification_attempts (id,user_id,kind,provider,status,provider_ref,match_score,failure_reason,response_body,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING *;
-- name: KYCAttemptsByUserID :many
SELECT * FROM kyc_verification_attempts WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2;
-- name: RecentKYCAttemptCount :one
SELECT count(*) FROM kyc_verification_attempts WHERE user_id=$1 AND kind=$2 AND created_at >= $3;
-- name: CreateKYCFacialCapture :one
INSERT INTO kyc_facial_captures (id,user_id,attempt_id,verification_request_id,selfie_image,liveness_frame_images,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING *;
-- name: LimitForTier :one
SELECT * FROM transaction_limits WHERE tier=$1 AND currency=$2;
-- name: EnsureLimitUsage :exec
INSERT INTO limit_usage (id,user_id,currency,window_type,window_key) VALUES (gen_random_uuid(),$1,$2,$3,$4) ON CONFLICT (user_id,currency,window_type,window_key) DO NOTHING;
-- name: LockLimitUsage :one
SELECT * FROM limit_usage WHERE user_id=$1 AND currency=$2 AND window_type=$3 AND window_key=$4 FOR UPDATE;
-- name: ApplyLimitUsageDelta :execrows
UPDATE limit_usage SET used_minor=used_minor+$2,used_count=used_count+$3,updated_at=now() WHERE id=$1 AND used_minor+$2>=0 AND used_count+$3>=0;
-- name: UsageForWindow :one
SELECT * FROM limit_usage WHERE user_id=$1 AND currency=$2 AND window_type=$3 AND window_key=$4;
-- name: UpsertTransactionLimit :one
INSERT INTO transaction_limits (id,tier,currency,single_transaction_max_minor,daily_max_minor,monthly_max_minor,max_balance_minor,daily_count_max,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (tier,currency) DO UPDATE SET single_transaction_max_minor=excluded.single_transaction_max_minor,daily_max_minor=excluded.daily_max_minor,monthly_max_minor=excluded.monthly_max_minor,max_balance_minor=excluded.max_balance_minor,daily_count_max=excluded.daily_count_max,updated_at=now() RETURNING *;
-- name: ListTransactionLimits :many
SELECT * FROM transaction_limits ORDER BY tier,currency;
