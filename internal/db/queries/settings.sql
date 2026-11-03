-- What a server's owner changes about it: its startup variables, its image and its name.

-- name: LockServer :one
SELECT id
FROM servers
WHERE id = ?
FOR UPDATE;

-- name: EggImagesOfServer :one
SELECT e.docker_images
FROM servers s
JOIN eggs e ON e.id = s.egg_id
WHERE s.id = ?;

-- name: VariableOfServer :one
SELECT ev.id
FROM egg_variables ev
JOIN servers s ON s.egg_id = ev.egg_id
WHERE s.id = ? AND ev.env_variable = ?;

-- name: UpdateServerVariable :execrows
UPDATE server_variables
SET variable_value = ?, updated_at = ?
WHERE server_id = ? AND variable_id = ?;

-- name: InsertServerVariable :exec
INSERT INTO server_variables (server_id, variable_id, variable_value, created_at, updated_at)
VALUES (?, ?, ?, ?, ?);

-- name: SetServerImage :execrows
UPDATE servers
SET image = ?, updated_at = ?
WHERE id = ?;

-- name: RenameServer :execrows
UPDATE servers
SET name = ?, description = ?, updated_at = ?
WHERE id = ?;

-- name: CountServerVariableRows :one
SELECT COUNT(*)
FROM server_variables
WHERE server_id = ? AND variable_id = ?;
