# M5 – the client's read endpoints

Status: **handlers done over storage interfaces** (2026-10-23): `internal/api/clientapi`. The database layer that fills the interfaces waits for the schema.

## Routes (all below `/api/client`, `docs/plan.md` 8)
| Route | Who | Answer |
|---|---|---|
| `GET /` | signed in (two-factor requirement applies) | the servers of the user, a page of `object: list` with `meta.pagination` |
| `GET /permissions` | signed in | `{"object":"system_permissions","attributes":{"permissions":{group: {description, keys}}}}`, never an `admin.*` name |
| `GET /account` | signed in, **open without two-factor** (the page where it is switched on) | `object: user`: `id`, `admin`, `username`, `email`, `first_name`, `last_name`, `language` |
| `GET /account/activity` | signed in | the entries about the user |
| `GET /servers/{server}` | access to the server, **in any state** | `object: server` with `meta.is_server_owner` and `meta.user_permissions` |
| `GET /servers/{server}/activity` | `activity.read`, normal state (409 while installing, suspended, ...) | the entries about the server |

## The server object
The attributes are the reference's, in its order. Three things depend on what the caller may do:
- **`invocation`** shows the values of the variables only to someone who holds `startup.read`, and then only of those a user may view; any other variable is `[hidden]`.
- **`allocations`** (always included) are all of them for `allocation.read`; without it only the primary one, with its notes cleared.
- **`variables`** (always included) are the viewable ones for `startup.read`, otherwise `{"object":"null_resource","attributes":null}`. A variable that is not viewable never appears, not in the answer and not in the command.
- `?include=egg` adds the egg; `?include=subusers` adds the subusers for `user.read`, otherwise a null resource. The store is asked for subusers only for servers whose subusers the caller may see, and only if asked.

A row of the list is judged by the permissions of the caller on **that** server: `permissions.Effective(admin, owner, the subuser entry the store attaches to the row)`. The page of one server takes the permissions the authorizer already worked out.

## The list
`?type=` picks the scope (`servers.ParseListScope`): the default is what the user owns or is a subuser of; `owner`; for an administrator `admin` (what they reach only as an administrator) and `admin-all`. An administrator's list asked by somebody else is **empty and the store is not asked**. Filters are `uuid`, `name`, `description`, `external_id` (partial) and `*` (the search); `sort` is not offered (400). The page is 50, at most 100 (a larger `per_page` is 422, C-035).

## Activity lists
Filter `event` (partial) and sort `timestamp`; 25 a page, at most 100. The store gets the events to leave out (`activity.HiddenEvents()`), whether the actors are wanted (`?include=actor`) and, for a server, whether to hide administrators (`activity.hide_admin_activity`). Every entry goes through `activity.Present` for the caller (C-051): the address only for the user who did it and for administrators, an address among the properties only for its owner.

## What matches the reference
The attribute names and order, the null resources, the scope rules, the filters and includes, the e-mail address of the actor and of a subuser in the user object (the frontend shows it in a tip), the Atom form of times (`+00:00`, never `Z`), `server_identifier` as `serv_` and the UUID in lower case base32, `user_` the same for users, the picture address of a user (`gravatar.com`, MD5 of the lower case address).

## Deliberate differences (`docs/compat.md`: C-035, C-051, C-055)
`identifier` is always the short UUID and `{server}` also takes the long form (C-055); a larger `per_page` is refused (C-035). A handler wired without the authorizer, or a server route without a server, answers 403 and reads nothing.

## Open points
- Someone who holds `activity.read` on a server sees the **e-mail address** of the users who did things there, as in the reference (the frontend shows it). If that is too much, the actor object could carry the address for administrators and for the actor themself only; the tip would then be empty for others.
- The rate limit of the client API (240 a minute per user in the reference) is a wiring matter: one limiter on the `/api/client` group when the server is assembled.

## Still to do (waits for the schema)
The stores: `Accounts.ByID`; `Servers.List` (the scope condition joined with `Query.Where()` and `OrderBy("id")`, one query for the allocations and variables of the page, the viewer's subuser entry per row), `One`, `Subusers`; `ActivityLogs.ForUser` / `ForServer` (the subject tables, the excluded events, the join for `hide_admin_activity`, the actors only when asked). Then the wiring in `raptor serve`.

Package name: `clientapi` (like `authapi`), not `api/client` as the plan's table says; the plan's file names (`servers.go`, `activity.go`) are kept, `permissions` and `account` are small enough to live in `servers.go` and `account.go`.
