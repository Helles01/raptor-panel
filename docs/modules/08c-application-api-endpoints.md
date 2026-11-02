# M8 / M5 – the Application API routes for users, locations, nodes, allocations, nests and eggs

Status: **handlers done over storage interfaces** (2026-11-02): `internal/api/appusers`, `applocations`, `appnodes`, `appallocs`,
`appnests`, and `internal/api/appkit` that they share. The rules are in `internal/users`, `internal/nodes`, `internal/servers`
(8b); the lists use `internal/api/listquery`. The servers of the Application API (M5 reads, M9 writes, M14 databases) are not here yet.

Every route is registered with `api.ApplicationKey(resource, level)`: an application key (`ptla_`) of an administrator, with at
least that level on the resource (C-054). A client key, a session or a key without the resource is refused before a handler runs, and
a handler called without the authorizer answers 403 (the "bare route" tests).

## What `appkit` holds
- The **records** the stores return (`User`, `Location`, `Node`, `Allocation`, `Nest`, `Egg`, `EggVariable`), with the relations an
  include fills.
- The **presentation** (`View.User`, `Location`, `Node`, `Allocation`, `Nest`, `Egg`): the attributes of the reference's transformers,
  times always in UTC (`+00:00`), a node never shows its token or token id, docker images keep the order of the egg (the first is the
  default), an egg that has no files shows `{}`, the egg's `config` and `script` includes are items whose `object` is `null`.
- **Includes**: `Wanted(query, spec)` reads `?include=` and keeps what is offered and shown; `Load(view)` is the part the store must
  load, namely what the key may read. What the key may not read is shown as a `null_resource` and never loaded. Servers are left out
  (C-064).
- `Caller`, `ID`, `List`, `Created` (201 with `meta.resource`), `StoreError` (not found 404, taken 422 "The email has already been
  taken.", anything else 500) and `FieldError`.

## Users (`/api/application/users`, resource `users`)
| Route | Level | What it does |
|---|---|---|
| `GET …`, `GET …/{user}`, `GET …/external/{id}` | read | filters `email uuid username external_id`, sorts `id uuid`, include `servers` (C-064) |
| `POST …` | write | `email username first_name last_name` are needed; `external_id password language root_admin` are optional. `users.PrepareAccount`; no password means a random one and a setup token (hash stored, plain in the mail); mail `AccountCreated`, entry `user:user.create` (email, username, admin) |
| `PATCH …/{user}` | write | the user is found first (404 before a bad body), then the four needed fields are checked; `users.PlanUpdate` (null `external_id` clears it, a language or `root_admin` not sent stays); the store carries out the effects in its transaction: sessions and node tokens after a password change, reset tokens after an address change, the application keys of someone who is no longer an administrator |
| `DELETE …/{user}` | write | `users.CheckCanDelete` is run by the store inside the transaction in which the servers are counted; 400 with the reference's text |

## Locations (`/api/application/locations`, resource `locations`)
Filters `short long`, sort `id`, include `nodes` (needs `nodes` read). `short` is needed on create and update; `long` stays as it is
on an update unless sent, and null or an empty text clears it (`nodes.ChangeLocation`). The unique `short` answers 422. A location
with nodes is not deleted (400).

## Nodes (`/api/application/nodes`, resource `nodes`)
| Route | Level | What it does |
|---|---|---|
| `GET …`, `GET …/{node}` | read | filters `uuid name fqdn daemon_token_id` (the last one exactly, C-044), sorts `id uuid memory disk`, includes `allocations` and `location` |
| `GET …/deployable?memory=&disk=&location_ids[]=` | read | `servers.FindViableNodes` over the usage the store reports; without `page` all of them, with `page` a page; none fits is 400 |
| `GET …/{node}/configuration` | **write** | the Wings configuration as JSON (`nodes.WingsConfigJSON`), the token in it, `Cache-Control: no-store`; it needs write access as in the reference |
| `POST …` | write | defaults of the node form for what is not sent (`nodes.DefaultNewNode`); a UUID and credentials are made here and the store seals the token; a location that is not there is 422 on `location_id` |
| `PATCH …/{node}` | write | `Settings.Apply` (the base path stays when not sent, C-044); `reset_secret` counts only as the JSON value `true`; the configuration is then sent to the node (C-063); when that fails the change stays and the answer is the reference's 400 |
| `DELETE …/{node}` | write | `nodes.CheckCanDeleteNode` inside the store's transaction |

## Allocations (`/api/application/nodes/{node}/allocations`, resource `allocations`)
`GET` (filters `ip port` exactly, `ip_alias` in part, `server_id` by owner: a number or none; no sort is offered; include `node` needs
`nodes` read), `POST` (`ip`, `ports` as a list of text, optional `alias`; `nodes.ExpandAllocations`; what its rules refuse is a 400
with the reference's text; existing address and port pairs are left as they are, 204), `DELETE …/{allocation}` (an allocation a server
uses is 400; an allocation of another node is 404). The node is looked for first, so an unknown node is 404 whatever the body is.

## Nests and eggs (`/api/application/nests`, resources `nests` and `eggs`)
Read only: `GET …` (a page; the reference takes no filters here and ignores them), `GET …/{nest}`, `GET …/{nest}/eggs` (all of them,
no paging), `GET …/{nest}/eggs/{egg}` (the egg is looked for in its own nest). Includes: nest `eggs`; egg `nest`, `variables`, `config`
and `script` (what an egg borrows from another, shown only when it borrows); the variables are shown as the model is, with times in
`…000000Z` form.

## Wings
`Client.UpdateConfiguration` sends the JSON configuration (`POST /api/update`); `appnodes.NewDaemon(*wings.Factory)` is the adapter.
`nodes.WingsConfigJSON` is the same configuration as the YAML one.

## Different from the reference (`docs/compat.md`)
C-063 (the new configuration of a node goes to the node's current address with its current token), C-064 (servers are not shown as an
include yet), and what 8b lists (C-044, C-045, C-046).

## Tests
Each package has the matrix of the keys (administrator or not, the level on the resource, a client key, no key), the shape of every
answer, the refusals with their texts and fields, failures of the store that must not reach the person, the "bare route" check and
`New`. Mutation spot checks on every package ended with all mutants killed once the missing cases were added (a null password, the
mail greeting, a zero memory in `deployable`, the order of the deployable list, the ranges of `location_ids`).

## Still to do
The stores (the unique keys mapped to `*appkit.TakenError`, the effects of a user change, the sealed node token, the paths mounted on
a node, the usage of a node), the servers of the Application API with their includes, the database hosts routes, and `raptor serve`
wiring of these groups.
