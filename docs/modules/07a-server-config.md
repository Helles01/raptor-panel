# M7 groundwork – permissions and server configuration

Status: **groundwork done** (2026-10-22): the pure logic behind the daemon API and the subuser endpoints, which needs no
database. The endpoints (`GET /api/remote/servers/{uuid}`, `/api/client/permissions`, `/servers/{server}/users*`) wait
for the schema, because they load the server, its egg, allocations, variables and mounts; they will fill the types of
`internal/servers` and call these functions.

## Done

### `internal/permissions`
| Piece | Content |
|---|---|
| `All()`, `Names()`, `Valid()` | The tree of 40 subuser permissions with the reference's descriptions, in the reference's order. `json.Marshal(All())` is the body of `GET /api/client/permissions` (an object keyed by group, order kept, which the frontend lists as is). |
| `Normalize()` | What is stored for a subuser: known permissions only, no duplicates, order kept, and `websocket.connect` always added (as the reference's `getDefaultPermissions`). |
| `Has()` | Permission check with the wildcard: `*` covers everything except `admin.*` names (as Wings). |
| `CanAssign()` | The subset rule: someone may give a subuser only permissions they hold themselves, and only assignable ones. `*` and `admin.*` are never assignable. |
| constants | One name per permission; `internal/wings` takes the permissions of its token scopes from here. |

A test compares the names with `reference/app/Models/Permission.php` (40 constants each).

### `internal/servers`
| File | Content |
|---|---|
| `server.go` | The input types the database layer fills: `Server`, `Allocation`, `Variable`, `Mount`, `Egg`. Values inherited from a parent egg (`config_from`) must be resolved by the caller. |
| `wingsconfig.go` | `Environment`, `Config` (the current format of `GET /api/remote/servers/{uuid}`), `Legacy` (the old format that egg placeholders refer to). Field names and types are those Wings v1.13.4 reads. |
| `startup.go` | `StartupCommand`: the command shown to users, with hidden variables as `[hidden]`. |
| `eggconfig.go` | `EggProcessConfiguration`: the `process_configuration` block (startup, stop, configs), with `{{server.…}}` and `{{env.…}}` filled in, the old-daemon placeholders rewritten and `{{config.…}}` left for the node. |

The tests include all 14 seeded eggs of the reference (each must build, and nothing the panel fills in may remain
unresolved) and the vanilla Minecraft egg end to end.

## What matches the reference
Everything that reaches the node, field by field (`ServerConfigurationStructureService`, `EnvironmentService`,
`EggConfigurationService`): the environment keys (`STARTUP`, `P_SERVER_LOCATION`, `P_SERVER_UUID`,
`P_SERVER_ALLOCATION_LIMIT`, with the panel's values winning over egg variables), the two server formats, the stop command
or `^SIGNAL`, the "done" string-or-list rule, the if-value replacements, the old placeholders
(`env.SERVER_MEMORY` and the others), and the text a value takes inside a string (`true` is `1`, `false` and null are empty).
The order of the egg's files and of the entries inside a `find` object is kept; a repeated key keeps the position of its
first appearance and the value of its last, as PHP does.

## Deliberate differences (`docs/compat.md`: C-019 to C-021)
- Placeholders are expanded in one pass, so a variable value cannot smuggle in another placeholder (C-019, C-021).
- Entries the node cannot read are left out instead of being passed on (C-019); empty lists and maps are never null or
  the wrong type (C-020).

## Still to do (waits for the schema)
- The database layer that fills `servers.Server` (one query set per server: egg with its parent, variables with the server
  values, allocations on the node, mounts), and the `/api/remote` handlers with the node scope enforced structurally (plan 10.1).
- Subuser endpoints (M10): the handlers use `Normalize` and `CanAssign`; the "no self-edit" rule and the revocation of
  tokens through the outbox are handler and outbox work.
- Cron next-run semantics for schedules (the day-of-month / day-of-week rule of the cron library) are not decided yet; the
  library's source has to be checked first.
