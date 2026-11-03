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
