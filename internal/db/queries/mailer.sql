-- What the panel sends mail with: the row of a setting, whose sealed value is the password of the mail server.

-- name: SettingRowID :one
SELECT id
FROM settings
WHERE `key` = ?;

-- The row of the setting is made empty if there is none, so that the password can be sealed under it, and its id is answered either way; a row that came from
-- the old panel keeps its value. One statement, so that two requests that both find no row take turns on the row instead of deadlocking on a shared lock.
-- name: EnsureSettingRow :execlastid
INSERT INTO settings (`key`, `value`)
VALUES (?, '')
ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id);

-- name: SettingRowIDForUpdate :one
SELECT id
FROM settings
WHERE `key` = ?
FOR UPDATE;
