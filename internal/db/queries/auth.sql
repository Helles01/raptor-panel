-- Users, API keys, sessions, resets and recovery tokens: what the authorizer and the sign-in endpoints read and write.

-- name: UserByID :one
SELECT id, uuid, username, email, language, root_admin, use_totp, totp_authenticated_at, password, created_at, updated_at
FROM users
WHERE id = ?;

-- The columns use utf8mb4_unicode_ci, so a name and an address are compared without regard to case.
-- name: UserByUsername :one
SELECT id, uuid, username, email, language, root_admin, use_totp, totp_authenticated_at, password, created_at, updated_at
FROM users
WHERE username = ?;

-- name: UserByEmail :one
SELECT id, uuid, username, email, language, root_admin, use_totp, totp_authenticated_at, password, created_at, updated_at
FROM users
WHERE email = ?;

-- A code of a step that is not later than the last one used is refused: the update changes a row only when it is.
-- name: UseTOTPStep :execrows
UPDATE users
SET totp_authenticated_at = ?
WHERE id = ? AND (totp_authenticated_at IS NULL OR totp_authenticated_at < ?);

-- name: SetUserPassword :execrows
UPDATE users
SET password = ?, updated_at = ?
WHERE id = ?;

-- name: RecoveryTokensForUser :many
SELECT id, token
FROM recovery_tokens
WHERE user_id = ?
ORDER BY id;

-- One use: only the request that deletes the row may use the token.
-- name: DeleteRecoveryToken :execrows
DELETE FROM recovery_tokens
WHERE id = ?;

-- name: LatestPasswordReset :one
SELECT token, created_at
FROM password_resets
WHERE email = ?
ORDER BY created_at DESC
LIMIT 1;

-- name: DeletePasswordResets :execrows
DELETE FROM password_resets
WHERE email = ?;

-- name: InsertPasswordReset :exec
INSERT INTO password_resets (email, token, created_at)
VALUES (?, ?, ?);

-- name: GetSession :one
SELECT id, user_id, ip_address, user_agent, payload, last_activity
FROM sessions
WHERE id = ?;

-- name: PutSession :exec
INSERT INTO sessions (id, user_id, ip_address, user_agent, payload, last_activity)
VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    user_id = VALUES(user_id),
    ip_address = VALUES(ip_address),
    user_agent = VALUES(user_agent),
    payload = VALUES(payload),
    last_activity = VALUES(last_activity);

-- name: DeleteSession :exec
DELETE FROM sessions
WHERE id = ?;

-- name: DeleteSessionsForUser :exec
DELETE FROM sessions
WHERE user_id = ?;

-- name: DeleteSessionsForUserExcept :exec
DELETE FROM sessions
WHERE user_id = ? AND id <> ?;

-- name: APIKeyByIdentifier :one
SELECT id, user_id, key_type, identifier, allowed_ips, last_used_at, expires_at,
       r_servers, r_nodes, r_allocations, r_users, r_locations, r_nests, r_eggs, r_database_hosts, r_server_databases
FROM api_keys
WHERE identifier = ?;

-- name: TouchAPIKey :exec
UPDATE api_keys
SET last_used_at = ?
WHERE id = ?;

-- name: ServerByUUID :one
SELECT s.id, s.uuid, s.uuidShort, s.owner_id, s.node_id, s.status, s.installed_at, n.name AS node_name, n.maintenance_mode,
       (SELECT COUNT(*) FROM server_transfers t WHERE t.server_id = s.id AND t.successful IS NULL) AS transfers
FROM servers s
JOIN nodes n ON n.id = s.node_id
WHERE s.uuid = ?;

-- name: ServerByShortUUID :one
SELECT s.id, s.uuid, s.uuidShort, s.owner_id, s.node_id, s.status, s.installed_at, n.name AS node_name, n.maintenance_mode,
       (SELECT COUNT(*) FROM server_transfers t WHERE t.server_id = s.id AND t.successful IS NULL) AS transfers
FROM servers s
JOIN nodes n ON n.id = s.node_id
WHERE s.uuidShort = ?;

-- name: SubuserPermissions :one
SELECT permissions
FROM subusers
WHERE server_id = ? AND user_id = ?;

-- name: NodeByTokenID :one
SELECT id, uuid
FROM nodes
WHERE daemon_token_id = ?;

-- name: SettingValue :one
SELECT `value`
FROM settings
WHERE `key` = ?;

-- name: InsertActivityLog :execlastid
INSERT INTO activity_logs (event, ip, description, actor_type, actor_id, api_key_id, properties, `timestamp`)
VALUES (?, ?, NULL, ?, ?, ?, ?, ?);

-- name: InsertActivitySubject :exec
INSERT INTO activity_log_subjects (activity_log_id, subject_type, subject_id)
VALUES (?, ?, ?);

-- name: NodesOfUser :many
SELECT s1.node_id FROM servers s1 WHERE s1.owner_id = ?
UNION
SELECT s.node_id FROM subusers su JOIN servers s ON s.id = su.server_id WHERE su.user_id = ?;

-- name: UserUUID :one
SELECT uuid
FROM users
WHERE id = ?;

-- name: NodeForDaemon :one
SELECT id, scheme, fqdn, daemonListen
FROM nodes
WHERE id = ?;

-- name: NodeOrigins :many
SELECT scheme, fqdn, daemonListen
FROM nodes
ORDER BY id;
