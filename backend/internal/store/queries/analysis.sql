-- name: CreateConversation :one
INSERT INTO conversations (user_id, title, platform, match_name)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetConversation :one
SELECT * FROM conversations WHERE id = $1;

-- Takes a row lock on the conversation, so a check of its analyses and the
-- insert that depends on it cannot interleave with a concurrent request.
-- name: LockConversation :one
SELECT * FROM conversations WHERE id = $1 FOR UPDATE;

-- name: ListConversationsForUser :many
SELECT * FROM conversations
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: CreateMessage :one
INSERT INTO messages (conversation_id, position, sender, body, sent_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListMessages :many
SELECT * FROM messages
WHERE conversation_id = $1
ORDER BY position;

-- name: CreateAnalysisResult :one
INSERT INTO analysis_results (conversation_id, status)
VALUES ($1, 'pending')
RETURNING *;

-- name: GetAnalysisResult :one
SELECT * FROM analysis_results WHERE id = $1;

-- name: GetLatestAnalysisForConversation :one
SELECT * FROM analysis_results
WHERE conversation_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: ListProfileEvidenceCandidates :many
SELECT a.id AS analysis_id, a.created_at AS analysis_created_at,
       c.id AS conversation_id, c.created_at AS submitted_at,
       (a.overall->'profile_evidence')::jsonb AS evidence,
       transcript.fingerprint, transcript.self_positions
FROM conversations c
JOIN analysis_results a ON a.conversation_id = c.id
CROSS JOIN LATERAL (
    SELECT sha256(convert_to(jsonb_agg(
               jsonb_build_array(m.sender, btrim(regexp_replace(m.body, '[[:space:]]+', ' ', 'g')))
               ORDER BY m.position
           )::text, 'UTF8'))::bytea AS fingerprint,
           coalesce(array_agg(m.position) FILTER (WHERE m.sender = 'self'), '{}')::integer[] AS self_positions
    FROM messages m
    WHERE m.conversation_id = c.id
) transcript
WHERE c.user_id = sqlc.arg(owner_id)
  AND c.created_at >= sqlc.arg(submitted_since)
  AND a.status = 'succeeded'
  AND a.overall->'profile_evidence'->>'rubric_version' = sqlc.arg(rubric)::text
  AND a.overall->'profile_evidence'->>'coverage' = 'eligible'
  AND (sqlc.narg(before_submission)::timestamptz IS NULL OR
       (c.created_at, c.id, a.created_at, a.id) <
       (sqlc.narg(before_submission)::timestamptz, sqlc.arg(before_conversation)::uuid,
        sqlc.arg(before_analysis_created)::timestamptz, sqlc.arg(before_analysis)::uuid))
ORDER BY c.created_at DESC, c.id DESC, a.created_at DESC, a.id DESC
LIMIT sqlc.arg(batch_size);

-- Claims a job for this delivery. Terminal rows return no row, so a redelivered
-- job can be acked without scoring the conversation again.
-- name: ClaimAnalysis :one
UPDATE analysis_results
SET status = 'running'
WHERE id = $1 AND status IN ('pending', 'running')
RETURNING *;

-- name: CompleteAnalysis :one
UPDATE analysis_results
SET status = 'succeeded',
    segments = $2,
    overall = $3,
    model_version = $4,
    completed_at = now(),
    error = ''
WHERE id = $1
RETURNING *;

-- name: FailAnalysis :one
UPDATE analysis_results
SET status = 'failed',
    error = $2,
    completed_at = now()
WHERE id = $1
RETURNING *;

-- Fails a run only while it is still waiting for a worker, so a publish whose
-- confirmation was lost cannot overwrite a row the worker already claimed.
-- name: FailPendingAnalysis :one
UPDATE analysis_results
SET status = 'failed',
    error = $2,
    completed_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: UpsertTrainingExample :one
INSERT INTO training_examples (conversation_id, label_source, outcome, reply_received, engagement_score, segment_labels, notes, consented)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (conversation_id, label_source) DO UPDATE
SET outcome = EXCLUDED.outcome,
    reply_received = EXCLUDED.reply_received,
    engagement_score = EXCLUDED.engagement_score,
    segment_labels = EXCLUDED.segment_labels,
    notes = EXCLUDED.notes,
    consented = EXCLUDED.consented
RETURNING *;

-- name: ListTrainingExamples :many
SELECT * FROM training_examples
WHERE consented
ORDER BY created_at
LIMIT $1 OFFSET $2;

-- name: CreateNotification :one
INSERT INTO notifications (user_id, kind, payload)
VALUES ($1, $2, $3)
RETURNING *;

-- name: MarkNotificationSent :exec
UPDATE notifications SET sent_at = now() WHERE id = $1;
