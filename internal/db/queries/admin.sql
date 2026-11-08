-- What the administrator's commands read and write: users, API keys, locations, nodes, nests, eggs, settings and database hosts.

-- name: InsertUser :execlastid
INSERT INTO users (external_id, uuid, username, email, name_first, name_last, password, language, root_admin, use_totp, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?);

-- name: ListUsers :many
SELECT id, uuid, username, email, root_admin
FROM users
ORDER BY id;

-- name: LockUser :one
SELECT id
FROM users
WHERE id = ?
FOR UPDATE;

-- name: CountOwnedServers :one
SELECT COUNT(*)
FROM servers
WHERE owner_id = ?;

-- name: DeleteUser :execrows
DELETE FROM users
WHERE id = ?;

-- name: ClearTwoFactor :execrows
UPDATE users
SET use_totp = 0, totp_secret = NULL, totp_authenticated_at = NULL
WHERE id = ?;

-- name: DeleteRecoveryTokensOfUser :exec
DELETE FROM recovery_tokens
WHERE user_id = ?;

-- name: DeleteSecretsOfRow :exec
DELETE FROM raptor_secrets
WHERE table_name = ? AND row_id = ?;

-- name: DeleteSecret :exec
DELETE FROM raptor_secrets
WHERE table_name = ? AND row_id = ? AND column_name = ?;

-- name: CountKeysOfUser :one
SELECT COUNT(*)
FROM api_keys
WHERE user_id = ?;

-- The token column is not null and means the old encrypted secret; a key made here has none, its hash is in raptor_secrets.
-- name: InsertAPIKey :execlastid
INSERT INTO api_keys (user_id, key_type, identifier, token, allowed_ips, memo, created_at, updated_at,
                      r_servers, r_nodes, r_allocations, r_users, r_locations, r_nests, r_eggs, r_database_hosts, r_server_databases)
VALUES (?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListAPIKeys :many
SELECT k.id, k.user_id, u.username, k.key_type, k.identifier, k.allowed_ips, k.memo, k.last_used_at, k.created_at,
       k.r_servers, k.r_nodes, k.r_allocations, k.r_users, k.r_locations, k.r_nests, k.r_eggs, k.r_database_hosts, k.r_server_databases
FROM api_keys k
JOIN users u ON u.id = k.user_id
WHERE (? = 0 OR k.user_id = ?)
ORDER BY k.identifier;

-- name: APIKeyForRevoke :one
SELECT k.id, k.user_id, u.username, k.key_type, k.identifier, k.allowed_ips, k.memo, k.last_used_at, k.created_at,
       k.r_servers, k.r_nodes, k.r_allocations, k.r_users, k.r_locations, k.r_nests, k.r_eggs, k.r_database_hosts, k.r_server_databases
FROM api_keys k
JOIN users u ON u.id = k.user_id
WHERE k.identifier = ?
FOR UPDATE;

-- name: DeleteAPIKey :exec
DELETE FROM api_keys
WHERE id = ?;

-- name: InsertLocation :execlastid
INSERT INTO locations (short, `long`, created_at, updated_at)
VALUES (?, ?, ?, ?);

-- name: LocationExists :one
SELECT COUNT(*)
FROM locations
WHERE id = ?;

-- name: ListLocations :many
SELECT l.id, l.short, l.long, (SELECT COUNT(*) FROM nodes n WHERE n.location_id = l.id) AS nodes
FROM locations l
ORDER BY l.id;

-- name: InsertNode :execlastid
INSERT INTO nodes (uuid, public, name, description, location_id, fqdn, scheme, behind_proxy, maintenance_mode, memory, memory_overallocate,
                   disk, disk_overallocate, upload_size, daemon_token_id, daemon_token, daemonListen, daemonSFTP, daemonBase, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?);

-- name: ListNodes :many
SELECT n.id, n.uuid, n.name, n.fqdn, n.scheme, n.daemonListen, n.location_id, (SELECT COUNT(*) FROM servers s WHERE s.node_id = n.id) AS servers
FROM nodes n
ORDER BY n.id;

-- name: InsertNest :execlastid
INSERT INTO nests (uuid, author, name, description, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: NestExists :one
SELECT COUNT(*)
FROM nests
WHERE id = ?;

-- name: InsertEgg :execlastid
INSERT INTO eggs (uuid, nest_id, author, name, description, features, docker_images, file_denylist, update_url, config_files, config_startup,
                  config_logs, config_stop, startup, script_container, script_entry, script_install, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertEggVariable :exec
INSERT INTO egg_variables (egg_id, name, description, env_variable, default_value, user_viewable, user_editable, rules, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpsertSetting :exec
INSERT INTO settings (`key`, `value`)
VALUES (?, ?)
ON DUPLICATE KEY UPDATE `value` = VALUES(`value`);

-- name: InsertDatabaseHost :execlastid
INSERT INTO database_hosts (name, host, port, username, password, max_databases, node_id, created_at, updated_at)
VALUES (?, ?, ?, ?, '', NULL, ?, ?, ?);

-- name: ListDatabaseHosts :many
SELECT h.id, h.name, h.host, h.port, h.username, h.node_id, (SELECT COUNT(*) FROM `databases` d WHERE d.database_host_id = h.id) AS `databases`
FROM database_hosts h
ORDER BY h.id;

-- name: DatabaseHostByID :one
SELECT id, name, host, port, username, node_id
FROM database_hosts
WHERE id = ?;

-- name: LockDatabaseHost :one
SELECT id, name, host, port, username, node_id
FROM database_hosts
WHERE id = ?
FOR UPDATE;

-- name: UpdateDatabaseHost :exec
UPDATE database_hosts
SET name = ?, host = ?, port = ?, username = ?, node_id = ?, updated_at = ?
WHERE id = ?;

-- name: CountDatabasesOnHost :one
SELECT COUNT(*)
FROM `databases`
WHERE database_host_id = ?;

-- name: DeleteDatabaseHost :execrows
DELETE FROM database_hosts
WHERE id = ?;

-- name: DeleteKeySecretsOfUser :exec
DELETE FROM raptor_secrets
WHERE table_name = 'api_keys' AND row_id IN (SELECT id FROM api_keys WHERE user_id = ?);

-- name: DeleteSecretsOfAllAPIKeys :exec
DELETE FROM raptor_secrets
WHERE table_name = 'api_keys';

-- name: DeleteSecretsOfAPIKeysOfType :exec
DELETE FROM raptor_secrets
WHERE table_name = 'api_keys' AND row_id IN (SELECT id FROM api_keys WHERE key_type = ?);

-- name: DeleteAllAPIKeys :execrows
DELETE FROM api_keys;

-- name: DeleteAPIKeysOfType :execrows
DELETE FROM api_keys
WHERE key_type = ?;
