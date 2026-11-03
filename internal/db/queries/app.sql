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

-- name: LockNode :one
SELECT id, uuid, daemon_token_id
FROM nodes
WHERE id = ?
FOR UPDATE;

-- name: UpdateNodeSettings :exec
UPDATE nodes
SET public = ?, name = ?, description = ?, location_id = ?, fqdn = ?, scheme = ?, behind_proxy = ?, maintenance_mode = ?, memory = ?, memory_overallocate = ?,
    disk = ?, disk_overallocate = ?, upload_size = ?, daemonListen = ?, daemonSFTP = ?, daemonBase = ?, updated_at = ?
WHERE id = ?;

-- name: UpdateNodeTokenID :exec
UPDATE nodes
SET daemon_token_id = ?, updated_at = ?
WHERE id = ?;

-- name: CountServersOnNode :one
SELECT COUNT(*)
FROM servers
WHERE node_id = ?;

-- name: DeleteNode :exec
DELETE FROM nodes
WHERE id = ?;

-- name: AllowedMountsOfNode :many
SELECT m.source
FROM mount_node mn
JOIN mounts m ON m.id = mn.mount_id
WHERE mn.node_id = ?
ORDER BY m.id;

-- name: NodeForConfig :one
SELECT uuid, fqdn, scheme, behind_proxy, daemonBase, daemonSFTP, daemonListen, upload_size, daemon_token_id
FROM nodes
WHERE id = ?;

-- name: UserCount :one
SELECT COUNT(*)
FROM users
WHERE id = ?;

-- name: ServerNodeAndUUID :one
SELECT uuid, node_id
FROM servers
WHERE id = ?;

-- name: UpdateServerDetails :exec
UPDATE servers
SET external_id = ?, owner_id = ?, name = ?, description = ?, updated_at = ?
WHERE id = ?;

-- name: LockAllocationsByID :many
SELECT id, node_id, server_id
FROM allocations
WHERE id IN (sqlc.slice('ids'))
ORDER BY id
FOR UPDATE;

-- The allocations are given to the server only if they are still free; the caller compares the number of rows with the number it asked for.
-- name: AssignAllocations :execrows
UPDATE allocations
SET server_id = ?
WHERE id IN (sqlc.slice('ids')) AND server_id IS NULL;

-- name: ReleaseAllocationsOfServer :exec
UPDATE allocations
SET server_id = NULL, notes = NULL
WHERE id IN (sqlc.slice('ids')) AND server_id = ?;

-- name: ReleaseAllAllocationsOfServer :exec
UPDATE allocations
SET server_id = NULL, notes = NULL
WHERE server_id = ?;

-- name: UpdateServerBuild :exec
UPDATE servers
SET memory = ?, swap = ?, disk = ?, io = ?, cpu = ?, threads = ?, oom_disabled = ?, database_limit = ?, allocation_limit = ?, backup_limit = ?, allocation_id = ?, updated_at = ?
WHERE id = ?;

-- name: UpdateServerStartup :exec
UPDATE servers
SET startup = ?, egg_id = ?, nest_id = ?, image = ?, skip_scripts = ?, updated_at = ?
WHERE id = ?;

-- name: EggNest :one
SELECT nest_id
FROM eggs
WHERE id = ?;

-- name: EggVariablesForRules :many
SELECT env_variable, name, rules, user_viewable, user_editable
FROM egg_variables
WHERE egg_id = ?
ORDER BY id;

-- name: VariableOfEgg :one
SELECT id
FROM egg_variables
WHERE egg_id = ? AND env_variable = ?;

-- name: InsertServer :execlastid
INSERT INTO servers (uuid, uuidShort, external_id, node_id, name, description, status, skip_scripts, owner_id, memory, swap, disk, io, cpu, threads, oom_disabled,
                     allocation_id, nest_id, egg_id, startup, image, allocation_limit, database_limit, backup_limit, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 'installing', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: DatabaseIDsOfServer :many
SELECT id
FROM `databases`
WHERE server_id = ?;

-- name: DeleteDatabaseRow :execrows
DELETE FROM `databases`
WHERE id = ?;

-- name: DeleteServerRow :exec
DELETE FROM servers
WHERE id = ?;

-- name: AllocationsOfNodes :many
SELECT id, node_id, ip, port, server_id
FROM allocations
WHERE node_id IN (sqlc.slice('ids'))
ORDER BY id;

-- name: LockServerDatabaseLimit :one
SELECT database_limit
FROM servers
WHERE id = ?
FOR UPDATE;

-- name: CountDatabasesOfServer :one
SELECT COUNT(*)
FROM `databases`
WHERE server_id = ?;

-- name: DatabaseHostExists :one
SELECT COUNT(*)
FROM database_hosts
WHERE id = ?;

-- name: DatabaseHostChoices :many
SELECT id, node_id
FROM database_hosts
ORDER BY id;

-- The password column is NOT NULL but holds nothing: the password is sealed in raptor_secrets.
-- name: InsertDatabase :execlastid
INSERT INTO `databases` (server_id, database_host_id, `database`, username, remote, password, max_connections, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, '', 0, ?, ?);

-- name: LockDatabase :one
SELECT id
FROM `databases`
WHERE id = ?
FOR UPDATE;

-- name: LockDatabaseOfServer :one
SELECT id
FROM `databases`
WHERE id = ? AND server_id = ?
FOR UPDATE;

-- name: TouchDatabase :exec
UPDATE `databases`
SET updated_at = ?
WHERE id = ?;
