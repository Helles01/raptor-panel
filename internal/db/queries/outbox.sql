-- The jobs of the outbox (migration 0002): a change that the world must hear of is stored with the change, and worked through afterwards.

-- name: EnqueueJob :exec
INSERT INTO raptor_outbox (kind, payload, run_at)
VALUES (?, ?, ?);

-- Several processes may claim at once: a row that another transaction has locked is skipped, not waited for.
-- name: DueJobs :many
SELECT id, kind, payload, attempts
FROM raptor_outbox
WHERE state = 'pending' AND run_at <= ? AND (lease_until IS NULL OR lease_until < ?)
ORDER BY id
LIMIT ?
FOR UPDATE SKIP LOCKED;

-- name: LeaseJob :exec
UPDATE raptor_outbox
SET lease_until = ?
WHERE id = ?;

-- name: DeleteJob :exec
DELETE FROM raptor_outbox
WHERE id = ?;

-- name: RetryJob :exec
UPDATE raptor_outbox
SET attempts = ?, run_at = ?, lease_until = NULL, failure = ?
WHERE id = ?;

-- name: FailJob :exec
UPDATE raptor_outbox
SET state = 'failed', attempts = ?, lease_until = NULL, failure = ?
WHERE id = ?;
