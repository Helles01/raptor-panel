-- What the client API reads: the account, the allocations, variables and subusers of servers, and the people behind activity entries.

-- name: AccountByID :one
SELECT id, root_admin, username, email, name_first, name_last, language
FROM users
WHERE id = ?;

-- name: ClientAllocationsOfServers :many
SELECT id, server_id, ip, ip_alias, port, notes
FROM allocations
WHERE server_id IN (sqlc.slice('ids'))
ORDER BY id;

-- name: ClientVariablesOfServers :many
SELECT s.id AS server_id, ev.name, ev.description, ev.env_variable, ev.default_value, sv.variable_value AS value,
       ev.user_viewable, ev.user_editable, ev.rules
FROM servers s
JOIN egg_variables ev ON ev.egg_id = s.egg_id
LEFT JOIN server_variables sv ON sv.variable_id = ev.id AND sv.server_id = s.id
WHERE s.id IN (sqlc.slice('ids'))
ORDER BY s.id, ev.id;

-- name: SubusersOfServers :many
SELECT su.server_id, su.permissions, u.uuid, u.username, u.email, u.use_totp, u.created_at
FROM subusers su
JOIN users u ON u.id = su.user_id
WHERE su.server_id IN (sqlc.slice('ids'))
ORDER BY su.server_id, su.id;

-- name: PeopleByID :many
SELECT id, uuid, username, email, use_totp, created_at
FROM users
WHERE id IN (sqlc.slice('ids'));
