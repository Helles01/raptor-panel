# The SQL layer, first part: authorizer, sign-in, sessions, pages, outbox

Status: **done and tested on a real MariaDB 11.8.9** (2026-11-03): `internal/store`, the queries `internal/db/queries/auth.sql` and `outbox.sql`, the migration
`0002_raptor_outbox`. The baseline of the reference's tables comes from its own schema dump (`internal/db/schema/0000_baseline.sql`, see `docs/testing-database.md`).

## What is implemented (22 interfaces)
| Area | Constructors of `store.DB` |
|---|---|
| Authorizer | `AuthzUsers`, `AuthzKeys`, `AuthzServers`, `AuthzNodes`, `AuthzSettings`, `AuthzAudit` |
| Sessions | `Sessions` |
| Sign-in | `AuthapiUsers`, `AuthapiRecovery`, `AuthapiResets`, `AuthapiNodes`, `AuthapiOutbox`, `AuthapiActivity` |
| Pages | `PageUsers`, `PageSettings`, `PageOrigins` |
| Outbox | `OutboxQueue`, `OutboxStore`, `OutboxNodes` |

An adapter is a small type of its own, because the same method name means different things in different packages; the constructor returns the interface it implements,
so a change of an interface is a compile error here.

## Rules the SQL carries (each is a test against the real database)
- **One code, one request**: `UseTOTPStep` is one `UPDATE … WHERE totp_authenticated_at IS NULL OR < step`; of 20 requests at once with the same step exactly one gets true, an earlier step never.
- **One use**: a recovery token and a reset link are spent by a `DELETE` whose affected rows tell who got it (20 at once: one). `Replace` deletes and inserts in one transaction, so an older link stops working.
- **The newest link counts** when the old panel left several for an address.
- **The outbox claims safely for several processes**: `SELECT … FOR UPDATE SKIP LOCKED` in a transaction that also writes the lease; 8 claimers over 5 jobs give each job to exactly one; a job whose lease passed is due again; `Retry` keeps the attempts and the failure text; `Fail` sets aside.
- **A user losing access is queued in one transaction** for every node where they own a server or are a subuser (`DeauthorizeUser`).
- **Server state**: `status`, the node's maintenance mode and an open transfer (`server_transfers.successful IS NULL`); an 8-character or 36-character reference only.
- **A damaged list restricts**: an unreadable `allowed_ips` is kept as one entry no address matches; unreadable subuser permissions give none.
- Time is UTC in and out; the activity log uses the reference's model name for users (`Pterodactyl\Models\User`) so old and new rows agree.

## Different from the reference
C-071 (secrets of keys and nodes only from `raptor_secrets`; an expired key is refused).

## Tests
`internal/store/integration_test.go` (build tag `integration`, `RAPTOR_TEST_DSN`): 14 tests. Mutants of the SQL killed (run against the database after `sqlc generate`): the TOTP step condition, the reset
delete, the order of the latest link, `SKIP LOCKED`, the state filter of the claim, the "except" of the session delete. Survivor that is equivalent: the prefix `LIKE` on an 8-character column.

## Still to do in the SQL layer
`clientapi` (3 interfaces), `accountapi` (13), `controlapi.Targets`, `remoteapi` (the node API, 10 + 8), `settingsapi`, the Application API stores (about 45 methods), `admin.Store` and the CLI
registration, `allocapi`, `subuserapi`, `scheduleapi`/`schedules`, `backupapi`, `databaseapi`, `maintenance`. For the first test: `clientapi`, `controlapi`, `remoteapi`, `admin.Store` and the stores the Application API needs to create a server.

## Second part (2026-11-03): the administrator's commands, now registered
`store.AdminStore`, `AdminDirectory`, `AdminHosts` and `DBHostLookup` (4 more interfaces, 26 of 66 in all), over `internal/db/queries/admin.sql`; and the commands themselves are now in the
program: `raptor user|apikey|location|node|nest|egg|settings|dbhost …` (`cmd/raptor/cmd_admin.go`). They read the configuration from `RAPTOR_CONFIG` and the `RAPTOR_*` variables (needed:
the database, `RAPTOR_SECRET_KEY`, `RAPTOR_APP_URL`); help and a mistyped name need no database.

- A user, a key, a node, a nest and an egg are made the way the sign-in and the authorizer read them: a test creates each through the command and then asks the authorizer's own store for it
  (the key found by its identifier with the hash of the secret the command showed; the node found by the token id, with the token that is in the configuration the command printed).
- The count of a user's keys and of their servers is taken with the user's row locked (`SELECT … FOR UPDATE`): 40 requests at once make exactly 25 keys.
- Deleting a user, a key or a host removes its sealed secrets in the same transaction; the database's cascades (keys, recovery tokens, subusers) are tested with foreign keys **on**.
- The egg's `docker_images` keeps the order of the file; `field_type` of an egg file's variables has no column in the schema and is ignored (the open point of 01b is closed).
- `dbhost create` really signs in: the end-to-end test points it at the test database itself; a wrong password gives MariaDB error 1045 and saves nothing. This is the first run of `MySQLDialer` against a real server.

Tests: `internal/store/admin_integration_test.go` (6 tests), `cmd/raptor/cmd_admin_integration_test.go` (the whole first installation in one test, 7 steps) and `cmd/raptor/cmd_admin_test.go`.

## Third part (2026-11-03): the control routes and the node's API
`store.ControlTargets`, `ControlActivity`, `RemoteServers`, `RemoteUsers`, `RemoteActivity`, `RemoteEvents`, `RemoteBackups`, `RemoteOutbox` (8 more interfaces, 34 of 66), over
`internal/db/queries/node.sql`. With them a Wings can be given its servers, report an installation, a backup and its activity, and the console can find the node of a server.
`remoteapi.OldNodes` is not SQL (it is a call to a node) and is made when the program is put together.

- **The server configuration is read in four queries for any number of servers** (the servers with their node's location, their egg and the egg they take their process configuration from; the allocations; the
  variables with the server's own values; the mounts). An egg with `config_from` uses the parent's files, startup and stop; with `copy_script_from` it installs with the other egg's script.
- **What a node may reach** is decided in the store and again by the handler (the tests of both use a store that leaks): a server on the node, or one whose open transfer starts or ends there; for a third node the server is not there.
- **One report, one effect** (each is a single conditional statement, tested with 10-20 requests at once): the result of an installation (only against the status it was made for), the end of a backup
  (only while `completed_at IS NULL`), the end of a restore (only while `restoring_backup`), the end of a transfer (in one transaction with the locked transfer row, the move of the server and the release of allocations).
- **A restart of a node (`Reset`)** changes only servers that still have the status the change was made against and belong to the node, and writes the `server:backup.restore-failed` entry, with the server and the
  backup as subjects, only for a server that changed. The backup that "was being restored" is the newest successful one that is not deleted (the reference does not record which one it was).
- **The node's token** is opened from `raptor_secrets` for every call; a node without one gives an error that says so (not "not found"), because a panel that cannot talk to a node has to be told.
- Soft-deleted backups and SSH keys (`deleted_at`) are never found.

Tests: `internal/store/node_integration_test.go` (13 tests). Mutants killed: every statement guard above, the node scope of the backup, the SFTP server and the reach/own checks, the deleted-key filter, the order of the "last backup",
the inheritance of the egg and of the script, the move and the release of a transfer, the empty (not nil) list of a subuser whose permissions are damaged.

## Still to do in the SQL layer (32 of 66)
`clientapi` (6 methods), `accountapi` (13), `settingsapi`, the Application API (appusers, applocations, appnodes, appallocs, appnests, appservers, appserverdbs: about 45 methods), `allocapi`, `subuserapi`, `scheduleapi`/`schedules`,
`backupapi`, `databaseapi`, `maintenance`, `secretmigration` (done earlier). For the first test the next ones are `clientapi` (the server list and details in the browser), `accountapi` (two-factor setup) and the Application API
for creating a server.

## Fourth part (2026-11-03): the client API, the account and the server settings
`store.ClientAccounts`, `ClientServers`, `ClientActivity`, `AccountStore`, `AccountNodes`, `AccountActivity`, `SettingsStore` (7 more interfaces, 41 of 66), over `client.sql`, `account.sql`
and `settings.sql`, and the two **dynamic** queries of the client API (`internal/store/client.go`).

- **The server list is built from the specs, never from the request.** The scope of the viewer (accessible / owner / admin-others / all / none) is a fixed condition; the filters and the order come from
  `listquery` (`Query.Where()` and `OrderBy("id")`), whose column names are an allow-list and whose values are arguments. A test sends `x' OR '1'='1`, `'; DROP TABLE servers; --`, `%` and `_` as filter values and finds no
  server and loses none. The row carries the permissions of the **viewer** as a subuser of that server (a join on the viewer, not on any subuser: a test has the owner of a server whose subuser is somebody
  else see no permissions).
- **`ScopeAdminOthers`** is what an administrator reaches only as an administrator: not their own, and not the ones they are a subuser of.
- **Activity lists** are by subject (`activity_log_subjects`, an `EXISTS` so that a subject listed twice does not repeat an entry), without the events no list shows, with the filter, the order, the pages, and
  the people behind the entries loaded in one query. `hide_admin_activity` hides an administrator's entries on a server unless they own it or are a subuser of it.
- **The account**: a new address deletes the reset links of the old one in the same transaction (and an address that is taken changes nothing, links included); a new password deletes the links of the account's
  address; a new two-factor secret is sealed in `raptor_secrets` (never in the old column) and forgets the last used code; switching it on replaces the recovery tokens and records the code that did it so it cannot
  sign in again; the key limit counts the **account** keys only, with the user's row locked (40 requests at once make exactly 25); an SSH key is removed softly, as in the reference, and adding the same
  fingerprint twice is refused even for 10 requests at once.
- **The server settings**: a variable's row is made once whatever the number of requests (server row locked; the rows are counted because an update that finds the same value changes no row); the status
  changes only from the status the change was made against; the egg's images keep the order of the egg.

Tests: `client_integration_test.go` (6), `account_integration_test.go` (9), `settings_integration_test.go` (1). Mutants killed: every scope condition, the hide-administrators rule and its exception for the owner and
the subusers, the subject type of the activity lists, the exclusion of events, both offsets, the open-transfer filter, the viewer join, the reset links of an address change and of a password change, the lock
of the key limit, the type of key in the three key queries, the soft-deleted SSH keys, the old recovery tokens, the forgotten code.

## Still to do in the SQL layer (25 of 66)
The Application API (appusers, applocations, appnodes, appallocs, appnests, appservers, appserverdbs: about 45 methods, needed to create the test server), `allocapi`, `subuserapi`, `scheduleapi` and `schedules`,
`backupapi`, `databaseapi`, `maintenance`, and the small `ActivityLog` of each of those packages.

## Fifth part (2026-11-03): the Application API, first half
`store.AppUsers`, `AppUsersOutbox`, `AppUsersActivity`, `AppLocations`, `AppAllocations`, `AppNests` (6 more interfaces, 47 of 66), over `app.sql` and the read layer
`internal/store/appread.go` + `appserver.go`: one fixed SELECT for each kind of record, used for one record and for a list alike, with the relations of `?include=` filled by the store and the records attached carrying
no relations of their own. A server of the API is read with the same configuration the node is told (`build`), so what `?include=servers` shows on a user, a location, a nest or an egg is what the node gets.

- **`users.external_id` is not unique in the schema** (an index, no unique key), and a test found the reference's rule missing from a plain INSERT. The store keeps the rule: the rows with the value are read `FOR UPDATE`
  in the transaction of the write (which locks the place in the index too); a loser of a deadlock is run again and then gets the honest answer, `*appkit.TakenError{external_id}`. Of 12 requests at once with the same value, one wins.
- **A user's update and its effects are one transaction**: the fields; the password (only when a hash is given); the reset links of the old address (a change of address or of password) and of the new one (a new password); the
  sessions of the user; the orders to the nodes where they have servers (outbox); the application keys and their sealed secrets when administrator rights are taken away. A refused update (a taken address) changes nothing and ends no session.
- **Setup token and user are one transaction** on creation; a taken username, address or external id is named by its field and nothing is stored.
- **Allocations** are added a few hundred rows to a statement with `INSERT IGNORE` (an address and port the node has are left as they are; 1,201 rows in a test), listed per node with the owner filter (`filter[server_id]=` is "free"),
  and deleted with the row locked while the check looks at who holds it. An extra allocation of a server has no foreign key to hold it back, only the check (a test found the first assertion of mine would have been satisfied by the
  foreign key of the primary allocation alone).
- **Eggs** keep the order of their images, and `?include=config|script` shows what an egg borrows from the egg it takes its configuration or script from.

Tests: `appmisc_integration_test.go` (10). Mutants killed: the three effects of a user update, `INSERT IGNORE`, the node scope of allocations, the nest scope of an egg, the config include, both delete checks (with their own error
returned), the `config_from` join, the email-only effect.

## Still to do in the SQL layer (19 of 66)
The nodes of the Application API (8 methods; the node record itself is read already), the servers (15: create with the choice of allocations, the build, the startup, the details with an owner change, suspension, delete),
the server databases (6), `allocapi`, `subuserapi`, `scheduleapi` and `schedules`, `backupapi`, `databaseapi`, `maintenance` and the small `ActivityLog` of each of those packages.

## Sixth part (2026-11-03): the Application API, second half
`store.AppNodes`, `AppServers`, `AppServerDatabases` and `ClientDatabases` (4 more interfaces, 51 of 66). The two database stores are one piece of code (`serverDatabases`) with two thin adapters, because
the administrator and the owner do the same things with different types and errors.

- **A transaction that locks first.** MariaDB 11.6.2 and later refuse (error 1020, "Record has changed since last read") to lock or change a row that another request changed after the transaction's snapshot, and the
  snapshot is fixed by the first plain read. Creating a server read the owner and the egg before it locked the allocations, and 9 of 10 concurrent requests for one allocation died with 1020. The rule now: **the
  rows the decision depends on are locked before anything else is read**, and 1020 is retried like a deadlock (the second attempt starts with a new snapshot). The isolation level was *not* changed: the
  `users.external_id` rule relies on the gap locks of `REPEATABLE READ`, which `READ COMMITTED` does not take.
- **A node's token and its id are one change.** New credentials replace the sealed token and `daemon_token_id` in the transaction of the settings; the old token id finds nothing at once. A node without a sealed token
  (a migrated one, until `raptor migrate-secrets`) is reported by its configuration and its connection, not answered with an empty token.
- **Creating a server** locks the allocations named (the primary and the extra ones), hands them to the check (`Check` sees who holds them), stores the server on the node of the primary allocation and assigns all of
  them or none; of 10 requests at once for one free allocation, one wins and the others get the check's answer. Deleting releases the allocations with their notes cleared and the rest goes through the cascades.
  **A server that still has databases is not deleted**: the rows of a database go after the database itself is dropped on its host, and removing them in the server's delete would leave it there with nothing pointing to it
  (the handler deletes them one by one first, as the reference does).
- **The short identifier is read from `uuidShort`**, not derived from the uuid: a test that stored a different one caught it.
- **A database's limit is checked with the server's row locked**: of 12 requests at once for a server that may have one more, one is made. The password is sealed in `raptor_secrets`; the column keeps an empty string.
  A taken name is `ErrNameTaken`; a taken generated user name is a plain error (the request can be made again, it makes another).

Tests: `appservers_integration_test.go` (4), `appdatabases_integration_test.go` (3), plus `TestAppNodes`. Mutants killed: the count check of the allocations a server takes, the order to the previous owner, the sealing of a
node's new token, its removal with the node, the release of a deleted server's allocations, the lock of the database limit, the server check of a database's removal.

## Still to do in the SQL layer (15 of 66)
`allocapi`, `subuserapi`, `scheduleapi` and `schedules`, `backupapi`, `maintenance` and the small `ActivityLog` of each of those packages (and the `ActivityLog` of the database routes).

## Seventh part (2026-11-03): the owner's routes, the scheduler and the housekeeping — the SQL layer is complete
`store.ClientAllocations`, `ClientSubusers` (+ `SubuserOutbox`), `ClientSchedules`, `SchedulesStore` (the scheduler's), `ClientBackups`, the four `Maintenance*` stores and the seven small activity logs
(`AllocationActivity`, `SubuserActivity`, `ScheduleActivity`, `BackupActivity`, `DatabaseActivity`, `FileActivity`, `SettingsActivity`), over `queries/owner.sql`. `TestEveryStoreHasAConstructor` (no database needed) reads
`app.Stores` and `*store.DB` by reflection: **every store of the panel has a constructor that returns exactly its interface**, except the call to a node (`RemoteapiOldNodes`, made in `app.Build`) and the secret
migration (`internal/db`). A store that is added to `app.Stores` without a constructor fails that test.

- **Allocations.** A new one is a free allocation on the server's address, picked at random as in the reference, or a new one with the port the request chooses out of the ports the node has on that address. The
  server's row is locked first, so the limit is checked against what the server has; the free ones are read with a lock over the node's allocations at that address, so **requests for one address take turns**
  (a test of two servers that choose a port at the same moment found that this already prevents the same port being chosen twice, which is why there is no retry for it). `SetPrimary` and `Release` both lock the server
  before the allocation: a test that runs them against each other keeps the primary allocation one the server holds. An empty note is stored as no note, as the reference turns empty text into null.
- **Subusers** have no unique key on (server, user). `Add` locks the server's row and looks for the entry under it; of 12 requests at once for one person, one is made. A new account is made in the same transaction
  (with its setup link); a taken user name or address is `ErrUsernameTaken`/`ErrEmailTaken` and nothing is stored. A change with `revoke` queues the order to the node in the same transaction.
- **Schedules.** Tasks of a schedule are changed with the schedule's row and then its tasks locked (always in this order). A new task is placed by the handler's rule under the lock (of 14 requests at once for a
  schedule that may have 5 tasks, 5 are made, with the places 1–5); moving a task shifts the ones in between; deleting one closes the gap. `Begin` is one conditional statement on `is_processing = 0` — a test found that
  the first version of the check was satisfied by "no row changed" when the same values were written, so it now also tries a start with a *different* next time. A stored expression that cannot be read is left out of
  `Due` (it has no next time) and is an error for a run by hand; `raptor audit` lists them.
- **Backups.** `Start` locks the server's row, reads the backups that count (not failed, not deleted) and the creation times since `StartRequest.Since` (the start of the throttle window, which the service now passes;
  zero when the throttle is off), asks the check, soft-deletes the backup it says to replace and inserts the new one, in one transaction. Delete is soft, as in the reference. The page is in the order of the ids.
- **Housekeeping.** `FailOrphans` follows the reference (unfinished and old, not deleted; the lock flag is not touched — the comment of the interface said "a locked one is left alone", which was wrong and is fixed).
  The activity log is deleted 5,000 rows at a time (a log of years is not cleared under one lock; its subjects go through the foreign key). Sessions are spent by `last_activity` or by the `created_at` of Raptor's payload,
  compared as text to the second — so the session manager now writes it **in UTC** (`TestPayloadCreatedAtIsUTC`); a payload that is not Raptor's has no creation time and only the idle rule reaches it.
- **Activity entries** have the server as a subject and the thing they are about as another (allocation, user, schedule and task, backup, database); the key a call came with is kept.

**Two things the tests taught about this MariaDB.** (1) The test rig now empties `schedules`, `tasks` and `tasks_log` (schedules of an earlier test were due in the next). (2) The lock tests do not tell a missing
`FOR UPDATE` from a present one on MariaDB 11.6.2+, because `innodb_snapshot_isolation` makes the loser of the race fail with 1020 and run again, which gives the right answer by itself. The same tests were run with
`SET GLOBAL innodb_snapshot_isolation = OFF` (the behaviour of MariaDB 10.x and MySQL 8, which an old panel's database may have): the code passes, and the mutants without the locks fail (6 granted for a limit of 3).
Do this again when a lock is touched.

Tests: `clientalloc_` (5), `clientsubuser_` (3), `clientschedule_` (5), `clientbackup_` (4), `housekeeping_` (3), `activitylogs_` (1), `coverage_test.go`. Mutants killed: the server lock of the allocation limit, of the backup
limit and of a new task's schedule (all three with snapshot isolation off), the primary check of a release, the server lock and the order of a subuser change, `is_processing = 0` of a start, the deleted-backup condition
of the orphan job, `<` of the session age, both directions of a task move and the gap after a deletion.

## Still to do
The SQL layer is complete. Next: `app.Build` (needs adapters: `settingsapi.Reinstaller`, `remoteapi.OldNodes`, `clientapi.Texts`, the tokens, the session and CSRF wiring), the background jobs, `raptor serve`, the route-inventory
test against the built handlers, then the Wings installation and the browser test.
