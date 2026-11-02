# M5 / M9 / M14 – the Application API routes for servers and their databases

Status: **handlers done over storage interfaces** (2026-11-02): `internal/api/appservers`, `internal/api/appserverdbs`, and the server
presentation and the includes of all resources in `internal/api/appkit` (08c). The rules are in `internal/servers` (09a, 09b) and
`internal/dbhosts` (14a). With this the Application API of the reference is covered, but for the database hosts, which have no routes
of their own in it.

Every route is registered with `api.ApplicationKey(resource, level)` (C-054); a handler called without the authorizer answers 403.

## Servers (`/api/application/servers`, resource `servers`)
| Route | Level | What it does |
|---|---|---|
| `GET …`, `GET …/{server}`, `GET …/external/{id}` | read | filters `uuid uuidShort name description image external_id`, sorts `id uuid`, the includes below |
| `POST …` | write | see "Making a server" |
| `PATCH …/{server}/details` | write | name and owner needed; external id and description keep their value unless sent, null clears (C-050); the new owner must exist (422 on `user`); when the owner changes the store records, in the same transaction, that the previous owner's tokens and SFTP access are to be revoked (an `outbox.Deauthorization` job) |
| `PATCH …/{server}/build` | write | see "Changing the build" |
| `PATCH …/{server}/startup` | write | `startup`, `egg`, `image`, `skip_scripts` and `environment` are needed; `servers.PlanStartup` against the variables of the egg named (422 on `egg` if it is not there); the problems of the values are all told at once, each on `environment.<NAME>`; the node is not told, as in the reference |
| `POST …/{server}/suspend`, `…/unsuspend` | write | `servers.PlanSuspension`; the status is changed only if it is still what was read (409 otherwise), the node is told to sync, and the status is put back if it cannot be |
| `POST …/{server}/reinstall` | write | `servers.CheckCanReinstall`; the server is marked installing (conditionally), the node reinstalls, and the mark is put back if the node refuses |
| `DELETE …/{server}` and `…/{server}/force` | write | the node first (a refusal stops it unless forced, or unless the node says it does not have the server), then each database is dropped on its host (a failure stops it unless forced), then the server's rows; any other word than `force` is 404 |

Reads see `container.environment` as the node does, so whoever may read servers may read the values of the variables, as in the
reference. The identifier of a server in a route is its row number.

### Making a server
The body is validated field by field, then `servers.NewServer.Validate`, the egg is looked up (422 on `egg`), the environment is checked
as an administrator's (every variable counts), and with a `deploy` object `FindViableNodes`, `ParsePortSpecs` and `SelectAllocation`
choose the allocation. Then one call to the store does the transaction of 09a: the allocations are locked and read, the closure
`CheckNewServerAllocations` runs on them, the server is inserted in the installing state on the node of the primary allocation, the
allocations are assigned only if still free, and the values of the variables are stored. A UUID or short UUID that is taken is made
again (five times). After the commit the node is told to create the server; if it refuses, the node is asked to delete it (in case it
half made it), the server is deleted, and the person gets the node's answer. A feature limit sent as null is stored as 0, as in the
reference.

### Changing the build
Decided inside one store transaction through a closure: the store locks the server and the allocations named, calls the closure with what
it read, and applies the answer (limits, feature limits, `oom_disabled`, the default allocation, the allocations to assign and to
release with their notes cleared). The closure applies `LimitsUpdate`, the feature limits and `PlanAllocationChange`. The node is told to
sync afterwards; if it cannot be reached that is only logged, as in the reference. Differences: C-065.

### Includes
`allocations` (needs `allocations`), `user` and `subusers` (`users`), `nest`, `egg`, `variables` (the egg's variables with the server's
own value as `server_value`, needs `servers`), `location`, `node`, `databases` (`server_databases`). Without the right a null resource is
shown and the store is not asked (C-064).

## Databases of a server (`/api/application/servers/{server}/databases`, resource `server_databases`)
| Route | Level | What it does |
|---|---|---|
| `GET …`, `GET …/{database}` | read | all of the server's databases without paging; includes `password` (the password in the clear, `no-store`) and `host` (needs `database_hosts`) |
| `POST …` | write | `database` (3 to 48 ASCII), `remote` (digits, `%` and `.`, at most 15), `host` (an id); the switch of the panel and the limit of the server apply (400 with the reference's texts); the user and a 24-character password are made, the row is stored inside the limit check, then the three statements run on the host; if the host refuses, what it made is dropped and the row goes. 201 with `meta.resource`; the password is in the answer only with `?include=password` |
| `POST …/{database}/reset-password` | write | stored first, set on the host after; the old one is put back if the host refuses; 204 |
| `DELETE …/{database}` | write | dropped on the host first; if it refuses the row stays; 204 |

A database is looked for among the server's own: another server's is 404. Differences: C-066.

## Wings
`Client.CreateServer` (`POST /api/servers`) and `Client.Sync` (`POST /api/servers/{uuid}/sync`); `appservers.NewDaemon(*wings.Factory)` is
the adapter, with `Reinstall` and `Delete` from before.

## Tests
`appservers` 95 % statement coverage, `appserverdbs` 92 %; the matrix of keys for every route, the shape of every answer, every
refusal with its text and field, the compensations (the node refusing a creation, a suspension, a reinstall; the host refusing a
database), the failures of the store that must not reach the person, the "bare route" check and `New`. Mutation spot checks ended with
every mutant killed once the missing cases were added (the default allocation among the server's own, the status a node answered
against the one the panel makes up, null as a value of a variable), except two equivalent ones: the password set on a record that is
only shown when asked for, and the cap of a loop that is capped again.

## Still to do
The stores (the transactions above, the unique keys as `*appkit.TakenError`, the outbox job written with the change of owner), the
database hosts' connections (`Hosts.Run`), and the `raptor serve` wiring.
