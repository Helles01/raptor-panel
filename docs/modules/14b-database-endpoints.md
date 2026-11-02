# M14 – the database routes of a server

Status: **handlers done over storage interfaces** (2026-10-26): `internal/api/databaseapi`. The rules and the SQL are in
`internal/dbhosts` (14a).

## Routes: `/api/client/servers/{server}/databases`

| Route | Permission | What it does |
|---|---|---|
| `GET …` | `database.read` | `server_database` objects (`id`, `host` {address, port}, `name`, `username`, `connections_from`, `max_connections`); `?include=password` adds each password for callers with `database.view_password` (others get a `null_resource`), and the answer is then `no-store`. Other includes are left out |
| `POST …` | `database.create` | `database` (3-48 characters: letters, digits, `-`, `_`), `remote` (the address the user may connect from; no quotes or spaces); the host is chosen by `dbhosts.ChooseHost`, the limit is checked in the store's transaction (`CheckCanCreate`, a null limit means no limit); 2 a minute per server; the answer shows the new password once |
| `POST …/{database}/rotate-password` | `database.update` | a new password, shown in the answer |
| `DELETE …/{database}` | `database.delete` | |

If the panel's switch for users making databases is off, creating is 400 with the reference's text. `AllowRandom` lets a server get a
database on a host that is not linked to its node when none is.

## Order of work, and what happens when the host fails
The host is another system, so the steps cannot be one transaction.
- **Create**: the row is stored (password sealed by the store) inside the limit check; then the three statements run on the host. If
  the host refuses, what may have been made is dropped there and the row is taken back, and the person gets the first failure.
- **Rotate**: the new password is stored first, then set on the host; if the host refuses the old password is put back, so the
  panel never shows a password the host does not have.
- **Delete**: dropped on the host first, then the row; if the host refuses the row stays and the request can be made again.

The statements are built by `dbhosts` with bound passwords and checked identifiers (a name never goes into SQL unchecked); the
database name and user carry the server's prefix (`s<id>_`, `u<id>_`), checked again before running.

The password is only ever read by the store when asked for (`Password`), never kept in a record, and the log lines of a failure do
not carry it.

Entries: `server:database.create|rotate-password|delete`, with the database's name.

## Different from the reference
- **C-061**: the `{database}` of the route and the `id` of the object are the plain row number, not an obscured identifier. A
  database is always looked up among the server's own, so the number is no secret.

## Tests
88 % statement coverage; killed mutants include the password-view check, the limit boundary, the throttle key (a second server), the
undoing of a failed creation and rotation, the order store-then-host, and the host chosen.

## Still to do
The store (sealing and opening passwords with the panel's key, the limit check under the lock), `Hosts.Run` over real connections
(each host has its own credentials, which come from the store), integration tests against a MariaDB.
