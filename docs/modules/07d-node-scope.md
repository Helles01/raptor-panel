# M7 groundwork – the node API's scope, transfers, server list and reset

Status: **groundwork done** (2026-10-22): the rules of the endpoints a node calls that are not covered by `07b-sftp.md` and `07c-daemon-reports.md`.
The endpoints and their lookups wait for the schema.

## Done: `internal/remote`
| Function | Content |
|---|---|
| `Scope{NodeID}`, `Owns`, `CanReadDetails` | The calling node, and what it may reach. `Owns`: a server that is on this node. `CanReadDetails`: the same, except that while the server is being transferred **both** nodes may read its configuration. A scope with no node reaches nothing. |
| `Transfer`, `Validate`, `Fail`, `Succeed` | A transfer between two nodes. Either node may report a failure (the new node's allocations are released). Only the **new** node may report success (the old allocations are released, the server moves, the old node is told to delete its copy). No transfer means a 409, a wrong node a 403, both with the reference's text. `Validate` is for when a transfer is created. |
| `ParsePaging`, `Window` | `page` and `per_page` of the list (default 50, at most 500), and the rows to read and the `meta` to answer with, in Laravel's paginator form (`current_page`, `from`, `last_page`, `per_page`, `to`, `total`; `from` and `to` are null on an empty page). |
| `ResetStatus(status, installedBefore)` | What a node's restart resets: a `restoring_backup` becomes normal; an `installing` becomes `install_failed`, or `reinstall_failed` when the server had been installed before (C-036); nothing else changes. |

## Which rule each endpoint uses
| Endpoint | Rule |
|---|---|
| `GET /api/remote/servers` | only servers of this node (a filter in the query), `ParsePaging`, `Window`; per server `uuid`, `settings` (`servers.Config`), `process_configuration` (`servers.EggProcessConfiguration`) |
| `POST /api/remote/servers/reset` | `ResetStatus(status, installed_at is set)` for the servers of this node; for a server that was restoring and has a recorded `server:backup.restore-started` for a backup, write `server:backup.restore-failed` |
| `GET /api/remote/servers/{uuid}` | `CanReadDetails(server.node, active transfer)` |
| `GET` and `POST …/install` | `Owns` (+ `servers.ApplyInstallReport`, `07c`) |
| `POST …/transfer/failure`, `…/transfer/success` | `Fail`, `Succeed` |
| `POST /api/remote/activity`, `POST /sftp/auth`, the backup endpoints | the server must be on this node (`Owns`; for activity and SFTP the lookup itself only searches this node's servers) |

**Structural enforcement (plan 10.1 e):** the handlers get their servers from a lookup that is bound to the calling node (`WHERE node_id = ?`), and the
only exceptions are the two transfer rules above. No handler of the node API fetches a server by UUID alone. The rules here are what that lookup is
tested against: a test per endpoint with a second node's token must get the 403 (M7's negative tests on a real Wings, M17).

## What matches the reference
Every rule above, including the transfer exception for the details, who may report what, the texts of the two errors, and that an interrupted
backup restore ends as a normal server.

## Deliberate differences (`docs/compat.md`: C-035, C-036)
A cap on `per_page`, the validation of a transfer's allocations, and what becomes of an installation that a restart interrupted.

## Decision of the project owner (2026-10-22): an interrupted installation is marked as failed
The reference makes a server that was **installing** when its node restarted look **normal**, so a half-installed server shows no sign of it. Raptor
marks it `install_failed` (`reinstall_failed` when it had been installed before). The owner has to reinstall in either case; this way they see why.
A `reinstall_failed` server still counts as installed and can be used meanwhile, an `install_failed` one is blocked until it is reinstalled, which is
allowed for it (also for a server whose egg scripts are skipped).

## Still to do (waits for the schema and a real Wings)
- The endpoints; the old-node deletion after a successful transfer (best effort, logged, as in the reference); the activity entry for a reset restore.
- Check against a real Wings v1.13.4 that it reads `meta.current_page` and `meta.last_page` as written here, that it handles an empty list, and the
  order in which it calls reset (it is called after all servers have booted).
