# What the SQL layer has to implement

Written 2026-11-02 from the interfaces in the code (`go/ast` over `internal/`), so that the work that waits for the schema has a size and a
list. Every handler package defines the storage it needs as small interfaces next to the handlers and is tested against an in-memory fake; the
SQL layer (`internal/db`, sqlc) implements them. **66 interfaces with 217 methods** need a database; the rest are the node (Wings), the signing of
tokens, the database hosts and the clock, which are not SQL.

An interface is read in the package that defines it: its comments say what a method must guarantee. What follows is the part that the fakes
cannot show: where one SQL statement or one transaction is required.

## The groups
| Area | Interfaces (package.Name, methods) | Notes for the SQL |
|---|---|---|
| Authorizer | `authz.Users` 1, `Keys` 2, `Servers` 2, `Nodes` 1, `Settings` 1, `Audit` 1 | `Keys.Touch` is at most once a minute; node lookup by the token id; the server lookup takes the 8-character UUID prefix or the whole UUID, lower case |
| Sign-in | `authapi.Users` 5, `RecoveryTokens` 2, `Resets` 3, `Nodes` 1, `Outbox` 1, `ActivityLog` 1 | **Conditional single statements**: `UseTOTPStep`, recovery-token `Delete`, reset `Consume` must be `UPDATE … WHERE …` and report whether a row changed (replay protection, one use) |
| Sessions | `session.Store` 5 (`Get`, `Put`, `Delete`, `DeleteForUser`, `DeleteForUserExcept`) | `sessions.payload` is Raptor's JSON (C-003); expired rows are deleted by the maintenance jobs |
| Pages | `pageapi.Users` 1, `Settings` 1 | read only |
| Account | `accountapi.Store` 13, `Nodes` 1, `ActivityLog` 1 | e-mail/password/2FA changes and their effects in one transaction; password change ends the other sessions |
| Client reads | `clientapi.Accounts` 1, `Servers` 3, `ActivityLogs` 2 | the server list scopes (own / all / other users'); the filters and sorts come from `listquery` as allow-listed `Where`/`OrderBy` strings with bound values |
| Control | `controlapi.Targets` 1, `ActivityLog` 1 | node of a server |
| Allocations | `allocapi.Store` 7, `ActivityLog` 1 | the rule closures (`Check`/`Plan`) run **inside** the transaction, with the server's allocations locked |
| Subusers | `subuserapi.Store` 8, `Outbox` 1, `ActivityLog` 1 | revocation job in the same transaction as the change (outbox) |
| Settings, startup | `settingsapi.Store` 5, `ActivityLog` 1 | |
| Schedules | `scheduleapi.Store` 9, `schedules.Store` 9, `ActivityLog` 1 | `Begin` is conditional (`processing = 0`); `ResetRunning` at start |
| Backups | `backupapi.Store` 6, `remoteapi.Backups` 3, `ActivityLog` 1 | `Start`: count, throttle and insert in one transaction with the server's backups `FOR UPDATE`; the pruned row (C-068) is deleted in it; `SetServerStatus(from, to)` is one conditional update |
| Databases | `databaseapi.Store` 7, `appserverdbs.Store` 6, `dbhosts.Lookup` 1 | create locks the server's databases for the limit; passwords are sealed (`raptor_secrets`), `Lookup.Conn` opens the host's |
| Node API | `remoteapi.Servers` 10, `Users` 2, `ActivitySink` 3, `EventLog` 1, `OldNodes` 1, `Outbox` 1 | every lookup is **bound to the calling node**; `Installed(from)` is conditional |
| Application API | `appusers.Store` 6, `applocations.Store` 5, `appnodes.Store` 8, `appallocs.Store` 4, `appnests.Store` 4, `appservers.Store` 15, + `Outbox`/`ActivityLog` | the closures again (`Check`, `Change`, `Plan`); `SetStatus(from, to)` conditional; user delete counts owned servers inside the transaction |
| Administration | `admin.Store` 10, `HostStore` 5, `DirectoryStore` 7, `secretmigration.Store` 3 | `ChangeHost` locks the row while the closure signs in to the host |
| Outbox | `outbox.Store` 4, `Queue` 1, `Nodes` 1 | `Claim` must be safe for several processes (lease) |
| Housekeeping | `maintenance.BackupStore`/`ActivityStore`/`ResetTokenStore`/`SessionStore` 1 each | `FailOrphans` leaves finished backups alone |

Eleven of the interfaces are the same `Log(…)` of the activity log with a different entry type per package, and five are `Outbox.Send`/`Enqueue`:
they are one table each and can share one implementation behind small adapters.

## What is not SQL
`api.Authorizer`, every `Daemon` (Wings: `controlapi`, `fileapi`, `backupapi`, `appservers`, `appnodes`; `controlapi.NewDaemon` is the adapter), every
`Tokens`, `Hosts` (`dbhosts.Executor`), `dbhosts.Dialer`/`Session`, `schedules.Clock`/`Executor`, `scheduleapi.Starter`, `settingsapi.Reinstaller`,
`outbox.Deauthorizer`/`Sender`, `taskexec.*`.

## Order of work when the schema arrives
1. `docs/schema/` → compare `migrations.txt` with `internal/db/expected_migrations.txt`; `sqlc.yaml`; the baseline.
2. The stores the first test needs: authz, session, authapi, pageapi, clientapi, accountapi (reads), controlapi, remoteapi, outbox, activity, settings, admin.
3. The rest in the order of `docs/plan.md` chapter 5.
4. `app.Build` (every handler from its stores) and `raptor serve`; then the `inventory` test can also run against real handlers.
5. Integration tests against a real MariaDB: the conditional updates (two requests at once), the transactions with locks, the host statements (14c).

## Done so far (2026-11-03)
34 of the 66 interfaces are implemented in `internal/store` and tested on a real MariaDB: the authorizer (6), sessions (1), sign-in (6), pages (3), outbox (3) and the
node/user lookups they need, the four of the administrator and the eight of the control routes and the node API (see the note). See `modules/20a-sql-first-stores.md` for what is left.
