# Database hosts: reaching them, and managing them from the command line

Status: **done over interfaces** (2026-11-02): `dbhosts.Executor` and `dbhosts.MySQLDialer`, `admin.HostService`, `admin.Directory`, and the
commands in `internal/admincli` (`dbhost …`, `apikey list|revoke`, `user list`, `location list`, `node list`, `settings get`). The stores
(`admin.HostStore`, `admin.DirectoryStore`, `dbhosts.Lookup`) wait for the schema; the commands are not registered in `cmd/raptor` yet.

## `dbhosts.Executor`: the `Hosts` of the database endpoints
`Run(ctx, hostID, statements)` finds the host (`Lookup`), signs in, runs the statements in order on **one** connection, stops at the first that
fails and closes the connection. One connection for each run, no pool: databases are made and dropped rarely, and a pool would only keep
connections open to hosts nobody is using. `RunOn` and `Test` take the connection directly (before a host is saved).

- The whole run has a time limit (default 1 minute, connecting included) and obeys the request's context.
- `Conn` prints as `user@host:port`, with `%v`, `%+v`, `%#v`, `%s` and in structured logs: the password cannot get into a log line by accident.
- **Errors keep no text from the host.** An error of the host becomes `*ServerError{Number}`, and the failure reaches the caller as
  `*HostError{Step, Total, Number}`; the text of such an error can repeat the part of the statement the host did not understand, and the
  statement holds a password. Errors of the network (refused, timed out) keep their text and cause, so `errors.Is(err, context.DeadlineExceeded)` works.
- The host name, port and user are checked (`Host.Validate`) before anything is dialled.

`MySQLDialer` uses `go-sql-driver/mysql`: no multiple statements, no local files, no cleartext passwords; TLS mode one of `false` (default),
`true`, `skip-verify`, `preferred` (the same words as the panel's own database); 10 s to connect, 30 s to read and write. The driver puts the
values into the statement itself, escaped (`InterpolateParams`), because hosts differ in which account statements they accept as prepared
statements; a password that goes into one is held to `[A-Za-z0-9!@=.+^]` (8-64) by `dbhosts.checkPassword` in any case. **To confirm on a real
MariaDB (integration test):** `CREATE USER … IDENTIFIED BY ?` and `ALTER USER CURRENT_USER() IDENTIFIED BY ?` through this path, with and without TLS.

## `admin.HostService`
| Operation | Rule |
|---|---|
| `CreateHost` | name, host, port, user and password checked; **the panel signs in first, and a host that does not answer is not saved** (the reference saves in a transaction and tests inside it; the result is the same, C-070) |
| `UpdateHost` | a patch: what is not sent stays; `NodeSet` + nil unlinks the node; the merged host is checked and signed in to before it is stored; the row is locked while that happens |
| `TestHost` | signs in to a stored host |
| `RotatePassword` | makes a password (the generator of the databases), signs in with the **old** one, runs `ALTER USER CURRENT_USER() IDENTIFIED BY ?` and stores the new one in the same locked step. `CURRENT_USER()` names no account, so it is right whatever the address part of the account is. Returns the password once |
| `DeleteHost` | refused while the host has databases (`ErrHostInUse`, with the count) |

If the host took the new password and the store could not keep it, the panel is locked out of the host. `RotatePassword` then returns a
`*HostMismatchError` that carries the new password (not in its text, only through `NewPassword()`); the command prints it on the standard
output, says on the standard error how to store it (`raptor dbhost update <id> --password-stdin`, which signs in with it first) and exits with
an error.

## `admin.Directory` and the read commands
`Users`, `Keys(userRef)`, `RevokeKey(identifier)`, `Locations`, `Nodes`, `Setting(key)`. A key is revoked by its **identifier** (the first 16
characters, all that is stored in the clear); if a whole key is pasted it is refused and **not repeated in the message** (a test found the
first version repeating it). `settings get` reads only the keys of `settings.Keys()`.

## The commands (`internal/admincli`, `Services` + `All`)
`dbhost create|list|test|update|rotate-password|delete`, `apikey list|revoke`, `user list`, `location list`, `node list`, `settings get`.
- A password is never an argument: `dbhost create` needs `--password-stdin` or, said out loud, `--no-password`; an empty line is refused.
- Lists are tables on the standard output; a value from the database is shown with control characters as `?` and tabs or line ends as
  spaces, so a name with an escape sequence in it cannot move the cursor or change the colours of the administrator's terminal.
- `parse` now goes on after the first argument that is not a flag: `dbhost update 3 --name x` works as well as `dbhost update --name x 3`; `--` ends the flags.
- `All(Services{…})` leaves out the commands of a part that is nil.

## Different from the reference
C-070. There is no admin page for hosts at all in Raptor (plan 6.5): this is the replacement.

## Tests
`dbhosts` 94 %, `admin` 95 %, `admincli` 96 %. Mutants killed: step numbers, stop at the first failure, closing, the time limit, the check of the
connection, the dropped text of the host, the password in `String`/`GoString`/`LogValue`, the interpolation and TLS settings of the driver, every
field of the patch, the node link/unlink, the test before saving, the rotation order and the mismatch flag, the delete boundary at one database, the
identifier pattern, the sanitising of cells, the sort order of every list, and the `--` handling. Not covered until the integration test: a real
connection that succeeds (`mysqlSession.Exec`/`Close`).
