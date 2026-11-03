-- A user's own account: e-mail, password, two-factor authentication, API keys and SSH keys.

-- name: AccountForSettings :one
SELECT id, uuid, email, password, use_totp
FROM users
WHERE id = ?;

-- name: SetUserEmail :execrows
UPDATE users
SET email = ?, updated_at = ?
WHERE id = ?;

-- name: UserEmail :one
SELECT email
FROM users
WHERE id = ?
FOR UPDATE;

-- name: ResetTotpAuthentication :exec
UPDATE users
SET totp_authenticated_at = NULL
WHERE id = ?;

-- name: EnableTwoFactor :execrows
UPDATE users
SET use_totp = 1, totp_authenticated_at = ?, updated_at = ?
WHERE id = ?;

-- name: InsertRecoveryToken :exec
INSERT INTO recovery_tokens (user_id, token, created_at)
VALUES (?, ?, ?);

-- name: AccountAPIKeys :many
SELECT id, identifier, memo, allowed_ips, last_used_at, created_at
FROM api_keys
WHERE user_id = ? AND key_type = 1
ORDER BY id;

-- name: CountAccountKeysOfUser :one
SELECT COUNT(*)
FROM api_keys
WHERE user_id = ? AND key_type = 1;

-- name: LockAccountKey :one
SELECT id, identifier, memo, allowed_ips, last_used_at, created_at
FROM api_keys
WHERE user_id = ? AND identifier = ? AND key_type = 1
FOR UPDATE;

-- name: AccountSSHKeys :many
SELECT id, name, fingerprint, public_key, created_at
FROM user_ssh_keys
WHERE user_id = ? AND deleted_at IS NULL
ORDER BY id;

-- name: CountSSHKeysWithFingerprint :one
SELECT COUNT(*)
FROM user_ssh_keys
WHERE user_id = ? AND fingerprint = ? AND deleted_at IS NULL;

-- name: InsertSSHKey :execlastid
INSERT INTO user_ssh_keys (user_id, name, fingerprint, public_key, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: LockSSHKey :one
SELECT id, name, fingerprint, public_key, created_at
FROM user_ssh_keys
WHERE user_id = ? AND fingerprint = ? AND deleted_at IS NULL
ORDER BY id
LIMIT 1
FOR UPDATE;

-- The reference deletes an SSH key softly, and so does this.
-- name: SoftDeleteSSHKey :exec
UPDATE user_ssh_keys
SET deleted_at = ?, updated_at = ?
WHERE id = ?;
