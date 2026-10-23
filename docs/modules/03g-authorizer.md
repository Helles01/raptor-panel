# M3 – the authorizer: who is calling and what they may do

Status: **done over storage interfaces** (2026-10-23): `internal/api/authz` implements `api.Authorizer` for every kind of route and is tested over HTTP with the real router and in-memory stores.
The stores wait for the schema.

## Done
| Where | Content |
|---|---|
| `authz.New(Deps)`, `Authorize(c, permission)` | The decision for the route's descriptor, in the order below. What it learns is left in the request: `authz.From(c)` gives the `Principal` (user, key or session, and on a server route the server and the user's permissions), `authz.NodeFrom(c)` the node. A handler never looks these up again. |
| `api.Permission` | Gained `State` (`StateNormal`, `StateAny`, `StateAdminConsole`, `StateReinstall`) for server routes and `NoTwoFactor` for the account routes; `ServerPermissionIn`, `WithoutTwoFactor`. The router refuses combinations that make no sense when a route is registered. |
| `auth.HashKeySecret` | How an API key's secret is stored (SHA-256, hex). |
| `apierr.TwoFactorRequired` | The 400 with the code the frontend reads. |

## The decision, by kind of route
- **Public:** nothing. **Guest:** refused for a signed-in session.
- **Signed in (and server and admin routes):** a bearer token is a key and decides alone, the cookie is ignored. A key: right shape, known identifier, secret compared in constant time, user exists, a client key (an application key is refused), address allowed (`auth:ip-blocked` is logged for a refusal; an unreadable list blocks), the last use stored at most once a minute. Without a bearer token: the session's user.
  Then the requirement of two-factor authentication (none, administrators, everybody) unless the route is exempt; then, for an admin route, `root_admin`.
- **Server routes:** the `{server}` must be eight or 36 characters of a UUID (anything else is not even looked up). Not found, and not reachable for this user, give the same 404. An administrator or the owner has access, anyone else needs a subuser entry. Then the **state** policy of the route, then the permission (403): in this order, as in the reference.
  | Policy | Used by | Allows |
  |---|---|---|
  | `StateNormal` | most routes | installed, not suspended, no maintenance, not restoring or transferring |
  | `StateAny` | `GET /servers/{server}` | any state, so that an installing server can still be looked at |
  | `StateAdminConsole` | the websocket | normal, and an administrator also when it is installing, failed, restoring or transferring, but never when suspended or under maintenance |
  | `StateReinstall` | reinstall | normal, and a failed first installation (C-048) |
- **Application key routes:** only a bearer application key (a session or a client key is refused), of a user who is still `root_admin`, with the requirement of two-factor authentication applied, and the level the route needs on its resource: the read bit, or the write bit for a write route (C-054).
- **Daemon routes:** `Authorization: Bearer <token id>.<token>`: missing is 401 with `WWW-Authenticate: Bearer`, a bad shape 400, an unknown id or a wrong token the same 403, the token compared in constant time (and against a dummy when the id is unknown).

## What matches the reference
The texts and codes of the refusals, the order access → state → permission, the state exemptions of the view and the websocket, the two-factor requirement and the account routes that are exempt, the key address check and its event, the node token format.

## Deliberate differences (`docs/compat.md`: C-054; C-040 for the lists of addresses, C-048 for reinstalls)
Client keys and sessions do not open the Application API, key secrets are hashed and compared in constant time, and a server reference that cannot name a server is not looked up.

## Still to do (waits for the schema)
The stores (`Users`, `Keys` with `Touch`, `Servers` with the subuser lookup, `Nodes` with the opened token, `Settings` for the two-factor level, `Audit` for `auth:ip-blocked`), and the endpoint groups that read `authz.From`: M4, M5, M6 and M7 are next. The server `{server}` binding of the Application API is by numeric id and is a handler concern.
