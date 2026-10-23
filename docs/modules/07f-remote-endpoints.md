# M7 – the node API

Status: **handlers done over storage interfaces** (2026-10-23): `internal/api/remoteapi`. The stores wait for the schema. The backup routes of the node API (`GET/POST /backups/{backup}` and `/restore`) belong to M13.

## Routes (all below `/api/remote`, all `api.Daemon()`: the token of a node, `id.token`)
| Route | What it does | Rules used |
|---|---|---|
| `POST /sftp/auth` | The node asks for every SFTP sign-in. Answer `{user, server, permissions}`. | `sftp.*` (07b), `auth.VerifyPassword`, `middleware.Tracker` |
| `GET /servers` | One page of the node's servers, `{data: [{uuid, settings, process_configuration}], meta}` | `remote.ParsePaging/Window`, `servers.Config`, `servers.EggProcessConfiguration` |
| `POST /servers/reset` | After a restart: restoring becomes normal (with `server:backup.restore-failed`), installing becomes failed (C-036) | `remote.ResetStatus` |
| `POST /activity` | The events the node collected | `activity.Process` (07c, 07e) |
| `GET /servers/{uuid}` | The configuration of one server; both nodes of a transfer may ask | `Scope.CanReadDetails` |
| `GET /servers/{uuid}/install` | The install script of the egg | `Scope.Owns` |
| `POST /servers/{uuid}/install` | The result of an installation | `servers.ApplyInstallReport` |
| `POST /servers/{uuid}/transfer/failure` and `/success` | The end of a transfer | `Transfer.Fail/Succeed` |

## How the node is kept to its own servers
Every method of the `Servers` interface takes the node and may find only what that node reaches: its own servers, and (for the configuration and the end of a transfer) the servers being moved to or from it. The handlers do not rely on that alone: each applies the rule of `internal/remote` to what the store returned (`Owns`, `CanReadDetails`, `Fail`, `Succeed`), and the tests run every route once with a store that ignores the node ("leak") to show that the handler refuses. A server that a node cannot reach is the same 403 whether it exists or not (C-057).

## SFTP sign-in, in this order
Body (400 on a bad type or empty fields) → the user name and server id (400 for a bad id; a name that cannot be one is a failed attempt under a name nobody has) → the counters (429 with `Retry-After`) → the user and the server of this node (a missing one costs the time of a password check, then the same 403) → the password (a failure is counted and logged as `auth:sftp.fail` for that user, with the client's address) or the key (registered: go on; valid but not registered: 403, **not** counted, since clients offer all their keys; unreadable: 403, counted) → the right and the state (`server:sftp.denied` is logged with the user as actor; a server that cannot be used is 409).

## Installation reports
Only a server that is waiting for one (installing, or suspended meanwhile) takes it (C-033). The status is stored only if it is still the status read (`Installed(…, from, …)`), so two reports at once cannot both win. The mail "server installed" is queued after the report is stored; a mail that cannot be queued is logged and does not undo it.

## Transfers
The end is stored in one transaction and only if the transfer is still running. After a success the old node is told to delete its copy, best effort: a failure is logged.

## What matches the reference
The routes, bodies and answers, the texts of the refusals, the transfer rules (who may report what), the paging form the node reads (`meta.current_page`, `last_page`), the SFTP answer and its 403 for every kind of failed sign-in.

## Deliberate differences (`docs/compat.md`: C-031, C-032, C-033, C-034, C-035, C-036, C-057)
Node-bound lookups with one 403 for unreachable servers, a broken egg leaves its server out of the list, the person's address in the SFTP entries, conditional installation reports.

## Still to do (waits for the schema)
- The stores: `Servers` (node-bound queries; `Installed`, `Reset` and `CompleteTransfer` as conditional updates in transactions), `Users` (name lookup, key fingerprints), `ActivitySink`, `EventLog`, the outbox, and `OldNodes` (the node row of the old node, then `wings.Client.Delete`, which exists).
- A real Wings v1.13.4 run: the `ip` it sends, the answers it expects, and that it keeps going when the list leaves a server out.
- The backup routes of the node API (M13).
