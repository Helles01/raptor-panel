-- What the panel sends mail with: the row of a setting, whose sealed value is the password of the mail server.

-- name: SettingRowID :one
SELECT id
FROM settings
WHERE `key` = ?;
