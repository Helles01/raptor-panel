-- What the node's API (Wings) and the control routes read and write about servers.

-- name: TargetOfServer :one
SELECT s.uuid, n.id AS node_id, n.scheme, n.fqdn, n.daemonListen
FROM servers s
JOIN nodes n ON n.id = s.node_id
WHERE s.id = ?;

-- The transfer that is still running, if any.
-- name: OpenTransferOfServer :one
SELECT id, old_node, new_node, old_allocation, new_allocation, old_additional_allocations, new_additional_allocations, archived
FROM server_transfers
WHERE server_id = ? AND successful IS NULL
ORDER BY id DESC
LIMIT 1;

-- name: LockOpenTransfer :one
SELECT id, old_node, new_node, old_allocation, new_allocation, old_additional_allocations, new_additional_allocations, archived
FROM server_transfers
WHERE server_id = ? AND successful IS NULL
ORDER BY id DESC
LIMIT 1
FOR UPDATE;

-- name: FinishTransfer :exec
UPDATE server_transfers
SET successful = ?, updated_at = ?
WHERE id = ?;

-- name: MoveServer :exec
UPDATE servers
SET node_id = ?, allocation_id = ?, updated_at = ?
WHERE id = ?;

-- name: ReleaseAllocations :exec
UPDATE allocations
SET server_id = NULL
WHERE id IN (sqlc.slice('ids'));

-- The servers a node holds, a page at a time, with what their configuration needs from the same row and from the node, the location and the egg
-- (and the egg it takes its process configuration from, if any).
-- name: ServersOfNode :many
SELECT s.id, s.uuid, s.name, s.description, s.status, s.startup, s.image, s.memory, s.swap, s.disk, s.io, s.cpu, s.threads, s.oom_disabled,
       s.skip_scripts, s.allocation_limit, s.allocation_id, l.short AS location_short, e.uuid AS egg_uuid, e.force_outgoing_ip, e.file_denylist,
       e.config_from, e.config_files, e.config_startup, e.config_stop,
       p.config_files AS parent_config_files, p.config_startup AS parent_config_startup, p.config_stop AS parent_config_stop
FROM servers s
JOIN nodes n ON n.id = s.node_id
JOIN locations l ON l.id = n.location_id
JOIN eggs e ON e.id = s.egg_id
LEFT JOIN eggs p ON p.id = e.config_from
WHERE s.node_id = ?
ORDER BY s.id
LIMIT ? OFFSET ?;

-- name: CountServersOfNode :one
SELECT COUNT(*)
FROM servers
WHERE node_id = ?;

-- name: ServerForNodeByUUID :one
SELECT s.id, s.uuid, s.name, s.description, s.status, s.startup, s.image, s.memory, s.swap, s.disk, s.io, s.cpu, s.threads, s.oom_disabled,
       s.skip_scripts, s.allocation_limit, s.allocation_id, s.node_id, s.installed_at, l.short AS location_short, e.uuid AS egg_uuid,
       e.force_outgoing_ip, e.file_denylist,
       e.config_from, e.config_files, e.config_startup, e.config_stop,
       p.config_files AS parent_config_files, p.config_startup AS parent_config_startup, p.config_stop AS parent_config_stop,
       u.email AS owner_email, u.username AS owner_username
FROM servers s
JOIN nodes n ON n.id = s.node_id
JOIN locations l ON l.id = n.location_id
JOIN eggs e ON e.id = s.egg_id
LEFT JOIN eggs p ON p.id = e.config_from
JOIN users u ON u.id = s.owner_id
WHERE s.uuid = ?;

-- name: AllocationsOfServers :many
SELECT id, server_id, ip, port
FROM allocations
WHERE server_id IN (sqlc.slice('ids'))
ORDER BY id;

-- name: VariablesOfServers :many
SELECT s.id AS server_id, ev.env_variable, ev.default_value, ev.user_viewable, sv.variable_value AS value
FROM servers s
JOIN egg_variables ev ON ev.egg_id = s.egg_id
LEFT JOIN server_variables sv ON sv.variable_id = ev.id AND sv.server_id = s.id
WHERE s.id IN (sqlc.slice('ids'))
ORDER BY s.id, ev.id;

-- name: MountsOfServers :many
SELECT ms.server_id, m.source, m.target, m.read_only
FROM mount_server ms
JOIN mounts m ON m.id = ms.mount_id
WHERE ms.server_id IN (sqlc.slice('ids'))
ORDER BY ms.server_id, m.id;

-- name: SFTPServer :one
SELECT s.id, s.uuid, s.node_id, s.owner_id, s.status, n.maintenance_mode,
       (SELECT COUNT(*) FROM server_transfers t WHERE t.server_id = s.id AND t.successful IS NULL) AS transfers
FROM servers s
JOIN nodes n ON n.id = s.node_id
WHERE s.node_id = ? AND s.uuidShort = ?;

-- The script that installs a server: the egg's own, or the one it copies from another egg.
-- name: InstallScriptOfServer :one
SELECT e.copy_script_from, e.script_container, e.script_entry, e.script_install,
       c.script_container AS copy_container, c.script_entry AS copy_entry, c.script_install AS copy_script
FROM servers s
JOIN eggs e ON e.id = s.egg_id
LEFT JOIN eggs c ON c.id = e.copy_script_from
WHERE s.id = ?;

-- The result of an installation is stored only if the server still has the status the report was made against.
-- name: SetInstallStatus :execrows
UPDATE servers
SET status = ?, updated_at = ?
WHERE id = ? AND (status = sqlc.arg(from_status) OR (status IS NULL AND sqlc.arg(from_status) = ''));

-- name: SetInstallStatusAndTime :execrows
UPDATE servers
SET status = ?, installed_at = ?, updated_at = ?
WHERE id = ? AND (status = sqlc.arg(from_status) OR (status IS NULL AND sqlc.arg(from_status) = ''));

-- name: ResettableServersOfNode :many
SELECT id, status, installed_at
FROM servers
WHERE node_id = ? AND status IN ('installing', 'restoring_backup')
ORDER BY id;

-- name: LastSuccessfulBackup :one
SELECT id, name
FROM backups
WHERE server_id = ? AND is_successful = 1 AND deleted_at IS NULL
ORDER BY completed_at DESC, id DESC
LIMIT 1;

-- name: ResetServerStatus :execrows
UPDATE servers
SET status = ?, updated_at = ?
WHERE id = ? AND node_id = ? AND status = ?;

-- name: BackupOfNode :one
SELECT b.id, b.server_id, s.node_id, s.uuid AS server_uuid, b.uuid, b.name, b.disk, b.upload_id, b.completed_at, b.is_successful, b.is_locked
FROM backups b
JOIN servers s ON s.id = b.server_id
WHERE b.uuid = ? AND s.node_id = ? AND b.deleted_at IS NULL;

-- Only a backup that has not ended can be ended.
-- name: CompleteBackup :execrows
UPDATE backups
SET is_successful = ?, is_locked = ?, checksum = ?, bytes = ?, completed_at = ?, updated_at = ?
WHERE id = ? AND completed_at IS NULL;

-- The id of the multipart upload a node was given addresses for; a backup that has ended keeps the one it had.
-- name: SetBackupUploadID :execrows
UPDATE backups
SET upload_id = ?, updated_at = ?
WHERE id = ? AND completed_at IS NULL;

-- name: FinishRestoreOfServer :execrows
UPDATE servers
SET status = NULL, updated_at = ?
WHERE id = ? AND status = 'restoring_backup';

-- name: UserForSFTP :one
SELECT id, uuid, root_admin, password
FROM users
WHERE username = ?;

-- name: FingerprintsOfUser :many
SELECT fingerprint
FROM user_ssh_keys
WHERE user_id = ? AND deleted_at IS NULL;

-- name: ServerIDsOfNode :many
SELECT uuid, id
FROM servers
WHERE node_id = ? AND uuid IN (sqlc.slice('uuids'));

-- name: UserIDsByUUID :many
SELECT uuid, id
FROM users
WHERE uuid IN (sqlc.slice('uuids'));

