-- The Application API's writes of users, locations and allocations.

-- name: UpdateUserFields :execrows
UPDATE users
SET external_id = ?, email = ?, username = ?, name_first = ?, name_last = ?, language = ?, root_admin = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteApplicationKeySecretsOfUser :exec
DELETE FROM raptor_secrets
WHERE table_name = 'api_keys' AND row_id IN (SELECT id FROM api_keys WHERE user_id = ? AND key_type = 2);

-- name: DeleteApplicationKeysOfUser :exec
DELETE FROM api_keys
WHERE user_id = ? AND key_type = 2;

-- name: UpdateLocation :execrows
UPDATE locations
SET short = ?, `long` = ?, updated_at = ?
WHERE id = ?;

-- name: LockLocation :one
SELECT id
FROM locations
WHERE id = ?
FOR UPDATE;

-- name: CountNodesOfLocation :one
SELECT COUNT(*)
FROM nodes
WHERE location_id = ?;

-- name: DeleteLocation :exec
DELETE FROM locations
WHERE id = ?;

-- name: NodeExists :one
SELECT COUNT(*)
FROM nodes
WHERE id = ?;

-- name: LockAllocation :one
SELECT id, server_id
FROM allocations
WHERE id = ? AND node_id = ?
FOR UPDATE;

-- name: DeleteAllocation :exec
DELETE FROM allocations
WHERE id = ?;

-- users.external_id has an index but no unique key, so the rule that it is unique is kept here: the rows are read FOR UPDATE, which locks the
-- place in the index too and makes a concurrent request with the same value wait for this one.
-- name: ExternalIDHolders :many
SELECT id
FROM users
WHERE external_id = ? AND id <> ?
FOR UPDATE;
