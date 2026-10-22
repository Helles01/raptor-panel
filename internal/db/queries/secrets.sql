-- name: ListSecretHashes :many
SELECT row_id, source_hash
FROM raptor_secrets
WHERE table_name = ? AND column_name = ?;

-- name: UpsertSecret :exec
INSERT INTO raptor_secrets (table_name, row_id, column_name, ciphertext, source_hash)
VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE ciphertext = VALUES(ciphertext), source_hash = VALUES(source_hash);

-- name: GetSecret :one
SELECT ciphertext
FROM raptor_secrets
WHERE table_name = ? AND row_id = ? AND column_name = ?;
