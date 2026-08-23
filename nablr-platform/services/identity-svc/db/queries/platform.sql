-- name: Tier3ApplicationByUserID :one
SELECT * FROM tier3_applications WHERE user_id=$1;
-- name: CreateTier3Application :one
INSERT INTO tier3_applications (id,user_id,status,nin_status,address_status,location_status,utility_document_status,address_text,address_line1,address_line2,city,state,country_code,postal_code,latitude,longitude,accuracy_meters,location_provider,location_mocked,location_captured_at,location_consent_at,reverse_geocoded_address,distance_meters,submitted_at,approved_at,reviewed_at,reviewed_by,rejection_reason,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30) RETURNING *;
-- name: UpdateTier3Application :one
UPDATE tier3_applications SET status=$2,nin_status=$3,address_status=$4,location_status=$5,utility_document_status=$6,address_text=$7,address_line1=$8,address_line2=$9,city=$10,state=$11,country_code=$12,postal_code=$13,latitude=$14,longitude=$15,accuracy_meters=$16,location_provider=$17,location_mocked=$18,location_captured_at=$19,location_consent_at=$20,reverse_geocoded_address=$21,distance_meters=$22,submitted_at=$23,approved_at=$24,reviewed_at=$25,reviewed_by=$26,rejection_reason=$27,updated_at=now() WHERE id=$1 RETURNING *;
-- name: CreateTier3Document :one
INSERT INTO tier3_documents (id,application_id,user_id,document_type,original_filename,issue_date,object_key,content_type,size_bytes,sha256,etag,upload_expires_at,uploaded_at,extracted_name,extracted_address,status,rejection_reason,created_at,reviewed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) RETURNING *;
-- name: Tier3DocumentByID :one
SELECT * FROM tier3_documents WHERE id=$1 AND user_id=$2;
-- name: EnqueueOutbox :one
INSERT INTO outbox_events (id,exchange,routing_key,event_type,aggregate_type,aggregate_id,payload,status,attempts,last_error,available_at,published_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING *;
-- name: PendingOutboxCount :one
SELECT count(*) FROM outbox_events WHERE status='pending';
-- name: ClaimOutboxBatch :many
SELECT * FROM outbox_events WHERE status IN ('pending','failed') AND available_at<=$1 ORDER BY available_at,id LIMIT $2 FOR UPDATE SKIP LOCKED;
-- name: MarkOutboxPublished :exec
UPDATE outbox_events SET status='published',published_at=$2,last_error='',updated_at=now() WHERE id=$1;
-- name: MarkOutboxFailed :exec
UPDATE outbox_events SET status='failed',attempts=attempts+1,last_error=$2,available_at=$3,updated_at=now() WHERE id=$1;
-- name: MarkOutboxDeadLetter :exec
UPDATE outbox_events SET status='dead_letter',attempts=attempts+1,last_error=$2,updated_at=now() WHERE id=$1;
-- name: ReplayOutboxEvent :exec
UPDATE outbox_events SET status='pending',attempts=0,last_error='',available_at=now(),published_at=NULL,updated_at=now() WHERE id=$1 AND status='dead_letter';
-- name: DeletePublishedOutboxBefore :execrows
DELETE FROM outbox_events WHERE status='published' AND published_at<$1;
-- name: RecordAuditLog :one
INSERT INTO audit_logs (id,actor_type,actor_id,action,entity_type,entity_id,before_state,after_state,request_id,ip_address,user_agent,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING *;
-- name: SearchAuditLogs :many
SELECT * FROM audit_logs WHERE (sqlc.narg(actor_id)::uuid IS NULL OR actor_id=sqlc.narg(actor_id)) AND (sqlc.narg(entity_type)::text IS NULL OR entity_type=sqlc.narg(entity_type)) AND (sqlc.narg(entity_id)::uuid IS NULL OR entity_id=sqlc.narg(entity_id)) AND (sqlc.narg(action)::text IS NULL OR action=sqlc.narg(action)) AND (sqlc.narg(from_time)::timestamptz IS NULL OR created_at>=sqlc.narg(from_time)) AND (sqlc.narg(to_time)::timestamptz IS NULL OR created_at<=sqlc.narg(to_time)) ORDER BY created_at DESC LIMIT $1 OFFSET $2;
-- name: CountAuditLogs :one
SELECT count(*) FROM audit_logs WHERE (sqlc.narg(actor_id)::uuid IS NULL OR actor_id=sqlc.narg(actor_id)) AND (sqlc.narg(entity_type)::text IS NULL OR entity_type=sqlc.narg(entity_type)) AND (sqlc.narg(entity_id)::uuid IS NULL OR entity_id=sqlc.narg(entity_id)) AND (sqlc.narg(action)::text IS NULL OR action=sqlc.narg(action)) AND (sqlc.narg(from_time)::timestamptz IS NULL OR created_at>=sqlc.narg(from_time)) AND (sqlc.narg(to_time)::timestamptz IS NULL OR created_at<=sqlc.narg(to_time));
