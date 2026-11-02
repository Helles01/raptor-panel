# M13 – the backup routes (client and node)

Status: **handlers done over storage interfaces** (2026-10-26): `internal/api/backupapi` (the client routes) and
`internal/api/remoteapi/backups.go` (the routes the node calls). The rules are in `internal/backups` (13a).

## Client routes: `/api/client/servers/{server}/backups`

| Route | Permission | What it does |
|---|---|---|
| `GET …` | `backup.read` | a page (`per_page` 1-50, default 20) of `backup` objects; `meta.backup_count` counts the backups that count against the limit (running and successful) |
| `POST …` | `backup.create` | name (default `Backup at <time>`), `ignored` (one pattern per line, C-027), `is_locked` only if the caller also has `backup.delete`; 2 per 600 s per server (`backups.Throttle`, counted over every backup the server ever started) and the limit (`backups.Admit`), both inside the store's transaction with the server's backups locked; the node is told after the row is stored, and the row is taken back if it refuses |
| `GET …/{backup}` | `backup.read` | one backup |
| `GET …/{backup}/download` | `backup.download` | `signed_url` to the node with a 15-minute token; only a finished, successful backup |
| `POST …/{backup}/lock` | `backup.delete` | toggles the lock; the entry says `lock` or `unlock` |
| `POST …/{backup}/restore` | `backup.restore` | `truncate` optional; 3 per 15 minutes per server; the server is marked `restoring` with one conditional update first (`SetServerStatus(from, to)`) so nothing else is done to it meanwhile, and the mark is undone if the node refuses |
| `DELETE …/{backup}` | `backup.delete` | the archive on the node first (a node that does not know it, 404, is no hindrance), then the row; a locked finished backup cannot be deleted, a failed one can |

A backup UUID that is not a UUID, or belongs to another server, is 404. The throttles are counted after the authorizer (C-058).
Entries: `server:backup.start|lock|unlock|delete|download|restore`; the node's reports below write `complete|fail|restore-complete|restore-failed`.

## Node routes: `/api/remote/backups/{backup}`

| Route | What it does |
|---|---|
| `GET …?size=` | the S3 upload addresses; only backups on the node's own disk exist here, so the answer is the reference's 400 "not an S3 compatible adapter" (C-060) |
| `POST …` | the node reports how a backup ended: `backups.Complete` (checksum type and value from allow-lists, a failed one is unlocked with no checksum and size 0); a backup that is already completed cannot be reported on again (C-026); the entries are written in the same step |
| `POST …/restore` | the node reports a restore ended; the server goes back to its normal state **only if it was restoring** |

Every lookup is bound to the calling node: a backup of another node's server, a backup that does not exist and one that cannot be a
UUID give the same refusal (C-057).

## Wings (`internal/wings/client.go`)
`CreateBackup`, `RestoreBackup`, `DeleteBackup` (and `Delete`, `Reinstall` for servers). The request code was folded into one `run`
with a `call` description (method, path, body, content type, output, raw output with a limit, long timeout).

## Different from the reference (`docs/compat.md`)
- **C-060**: only the `wings` disk. A backup on S3 is shown in the list, but download, restore and delete refuse it with a clear 400;
  creating one is not offered.
- C-026/C-027 (13a), C-058 (the throttle is counted for authorized calls only).

## Tests
`backupapi` ~88 % and `remoteapi/backups.go` 94 % statement coverage. Mutants that were killed: the throttle and limit boundaries,
the lock-only-with-`backup.delete` rule, the restore throttle key, the restore status mark and its undoing, the 404-tolerant
delete, the check of the owner node, a second report, the restore report on a server that is not restoring. One survivor is
equivalent: the download's `backup.download` check is also made by the token maker.

## Still to do
The store (the transaction with `SELECT … FOR UPDATE`, the conditional status update), S3 (C-060), integration tests against a
real Wings and MariaDB.
