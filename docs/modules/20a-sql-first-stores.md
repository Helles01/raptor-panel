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
