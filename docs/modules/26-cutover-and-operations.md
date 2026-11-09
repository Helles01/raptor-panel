# 26 — Tools for the cutover and for running the panel (ten small modules)

Written 2026-11-09, at the owner's word ("csinálj 10 modult"). Nothing here is a new endpoint: these are the pieces that were missing around the panel, so that the day the old panel is
replaced (`docs/cutover.md`) and the weeks after it are done with commands that were tried, not by hand.

| # | Module | Where |
|---|---|---|
| 1 | The prune job aborts the open S3 uploads of backups that never finished | `internal/maintenance`, `internal/store/clientbackup.go` |
| 2 | `web.reload`: a new frontend build is served without a restart (development) | `internal/app/frontend.go`, `internal/api/web/shell.go` |
| 3 | The switches of the reference are the `features` section of the configuration | `internal/config/features.go` |
| 4 | `raptor backups check`: tries the S3 settings against the bucket | `internal/s3/check.go`, `cmd/raptor/cmd_backups.go` |
| 5 | `raptor doctor`: looks at the configuration, the key, the frontend, the database, the nodes | `internal/doctor`, `cmd/raptor/cmd_doctor.go` |
| 6 | `deploy/`: unit file, environment example, proxy examples, a README | `deploy/` |
| 7 | `raptor user reset-2fa --all` | `internal/admin`, `internal/admincli` |
| 8 | `raptor dbhost rotate-all` | `internal/admin/dbrotation.go` |
| 9 | `raptor migrate-secrets --finalize` | `internal/secretmigration/finalize.go` |
| 10 | The runbook `docs/cutover.md` and the read-only review `docs/cutover-review.sql` | `docs/` |

## 1 — Uploads that never finished
A multipart upload that is never completed or aborted stays in the bucket and its parts are paid for. The prune job (a background job of `raptor serve`, every half hour) already marked backups that never ended as
failed. It now also lists their open uploads (`FailOrphans` takes the rows `FOR UPDATE` inside the transaction, so a report from the node that arrives meanwhile either wins or loses cleanly) and aborts each one
on S3 **after** the transaction. The report says how many were aborted and how many could not be (`N open uploads on S3 were aborted[ and M could not be]`); a failed abort is not an error of the job, because
the row is already marked and the next run does not see it again, so the number is the only trace and the bucket's lifecycle rules are still the safety net.
- **Reference:** none; the reference leaves them to the bucket's lifecycle rules. **Different on purpose:** the panel does what it can itself, and keeps the rest to the bucket.
- **Tests:** unit (the aborter is called for each upload, a failing aborter is counted and not fatal, no aborter means no S3 call), and on the real database that the list is exactly the uploads of the rows that were marked.

## 2 — `web.reload`
`raptor serve` reads the frontend's `manifest.json` once at start (`22-visual-identity.md` says what happened when it was forgotten). With `web.reload: true` (env `RAPTOR_WEB_RELOAD`) a loop looks at the
manifest every two seconds (size and modification time) and gives the page shell the new file names and integrity values (`Shell.Reload`, guarded by a lock). A manifest that is half written or wrong is
**not** taken: the old one stays and one warning is logged for each bad version. It is off by default and meant for development; the production way is still a restart.
- **Tests:** a new build is served after the file changes, a bad manifest keeps the old page and warns once, the loop stops with the context; the shell is read and reloaded at the same time under the race detector.

## 3 — The `features` section
The constants of `internal/app/build.go` (databases for users, the random host, hiding the activity of administrators, the throttle of backups, the notices of finished installations) are now
`features.databases`, `databases_random_host`, `hide_admin_activity`, `backup_throttle_limit`, `backup_throttle_seconds`, `notify_install`, `notify_reinstall`, with env `RAPTOR_FEATURES_*`. The defaults are the
reference's values (databases on, a random host allowed, administrators' activity shown, a throttle of 2 backups in 600 seconds, both notices on), so a panel with no `features` section behaves as before.
`Validate` refuses a limit outside 1 to 1000 and a period over a day (a period of 0 turns the throttle off), and the start stops with the reason.
- **Matched against the reference:** `config/pterodactyl.php` and `config/app.php` (`PTERODACTYL_CLIENT_DATABASES_ENABLED`, `…_ALLOW_RANDOM`, `PTERODACTYL_HIDE_ADMIN_ACTIVITY`, `BACKUP_THROTTLE_LIMIT`/`PERIOD`).
- **Different:** they are read from the environment or the ignored file, not from the database settings, so a change is a restart; this is what the owner chose for everything that is not a setting of the admin UI.
- **Tests:** defaults, every variable, every refusal; the end-to-end test of the S3 flow runs with `hide_admin_activity` on and checks that a second administrator's entry is hidden from the first.

## 4 — `raptor backups check`
Tries the bucket the way the panel, a node and a browser use it, with an object of its own (a name made for the test, never one of a backup): start an upload, sign an address for a part, send a part through the
address (as the node would, with no credentials), put the parts together, read it back through a signed address (as a browser would) and compare, delete it. Every step is printed with `ok` or the reason; the
object is removed even when a step before the last fails. It needs no database, only the `RAPTOR_BACKUPS_S3_*` settings, and prints no key and no signed address.
The first run against a real bucket is the VPS test; this is the command to run first.
- **Different from the reference:** the reference has no such command; a wrong bucket shows on the first backup.
- **Tests:** against a test bucket that verifies both kinds of signature from what arrived, for each step failing in turn (a refused part, a corrupted read), and that the object is gone afterwards.

## 5 — `raptor doctor`
Checks, in this order, and prints `ok`, `warn` or `FAIL` for each: the **configuration** (what `serve` would refuse, the trusted proxies, development options left on, S3 settings), the **address** (the listen address can be
bound), the **secret key**, the **frontend build** (manifest and files), then, if the database can be reached: the **connection**, the **schema** (the 195 migrations of the reference and Raptor's own), **administrators**
(at least one), **old secrets** (values of the old panel that are not converted yet, found **without decrypting**), the **nodes** (each has a token) and **mail**. A database that cannot be reached is one `FAIL` and the checks
that need it are shown as skipped. The exit code is 1 only when there is a `FAIL`; warnings are for reading. It prints no secret: the checks look at whether a value is there, never at the value.
- **Different from the reference:** the reference has `p:environment:*` and `p:upgrade`, which do not look at the health of an installation.
- **Tests:** table-driven for every check; on the real database for the schema, the old secrets and the nodes (found on the way: a listen port of 0 in a configuration is invalid and the test now uses a free port).

## 6 — `deploy/`
`raptor.service` (an unprivileged user, `ExecStartPre=… raptor doctor`, a hardened sandbox: no new privileges, a read-only system, private temporary directory, only the directories it writes to), `raptor.env.example`
(every variable the code reads, with no secret in it), `nginx.conf.example` and `Caddyfile.example` (WebSocket upgrade for the console, the client address passed on in the way `trusted_proxies` expects), and `README.md`
(the order of the setup, the alias that runs `raptor` with the service's environment).
- **Tests (`deploy/deploy_test.go`):** every `RAPTOR_*` variable in the code is in the example and the other way round, the example values are accepted by the configuration loader, no secret is in any file, the unit
  keeps its hardening lines, both proxy examples pass `$remote_addr`. Six mutants made on purpose (a variable dropped from the example, a secret added, a hardening line removed, …) were all caught.

## 7 — `raptor user reset-2fa --all`
Clears the second factor of every user: `use_totp`, the sealed secret, the recovery tokens. Without `--yes` it only says how many users it would change. `--require none|admins|everyone` sets the existing
setting `pterodactyl:auth:2fa_required` (0, 1, 2) in the same run, so people who must have a second factor are asked to set it up at their next sign-in. The secrets of the old panel were readable with its key, so
after a leak they count as known (`docs/plan.md` 13.3).
- **Matched against the reference:** the reference has no bulk reset; one user disables it with their password (`DELETE /api/client/account/two-factor`), and the requirement setting is the one `PterodactylSettings` / `RequireTwoFactorMiddleware` use.
- **Tests:** unit (nothing changes without `--yes`, the count is shown, bad values of `--require`), and on the real database that the users, tokens and sealed secrets are all gone and other users' rows, sessions and API keys are not touched. Six mutants caught.

## 8 — `raptor dbhost rotate-all`
Sets a new password on **every database of every server** (or of one host with `--host N`): first on the database host, then in Raptor's store. It continues after a failure and lists the databases that failed
with the stage (`host` or `store`), so a failed one is still working with its old password and can be run again. It needs `--yes`; without it it shows how many databases it would change. `--host 0` is a usage error.
This is separate from `dbhost rotate-password` (the account the panel uses on the host), because it breaks every game server that has the old password written in a configuration file; `docs/cutover.md` says so before it is used.
- **Matched against the reference:** `DatabasePasswordService::handle` (a new random password, `ALTER USER`, then the row); the same order and the same operation, for all rows.
- **Tests:** unit (the order host then store, a host failure keeps the store as it was, a store failure is reported with its stage, `--host` selects, `--yes`), on the real database that the sealed password is the one that was set on the host. Six mutants caught.

## 9 — `raptor migrate-secrets --finalize`
Makes the old encrypted columns empty, **after** it has proved the values are safe in Raptor's own table. For every old value it checks: a converted copy exists, it was made from this very value (the hash recorded at
conversion matches), and it opens with this panel's key. If any one is missing, it says `NOT READY` with the table, the id and the reason, and changes nothing. Without `--yes` it only reports what would be emptied.
Doing it needs `--yes --backup-taken` (the owner says in the command that a backup of the database exists), and it needs no legacy key. Each row is emptied with `UPDATE … WHERE SHA2(column, 256) = ?`, so a value that
was changed after the check is left alone, and the whole run is one transaction; any row that does not change exactly once rolls it all back.
- **Different from the reference:** none to match; the reference never converts anything. This closes `docs/plan.md` 13.2.
- **Tests:** unit (every refusal, the order, the report), on the real database (`TestSecretStoreNeutraliseKeepsWhatWasChanged`), and the whole flow in `cmd/raptor` on a database with every kind of old value: not ready before the conversion, ready after, a wrong key refuses, the panel reads exactly the same values afterwards. Five mutants caught.

## 10 — The runbook
`docs/cutover.md`: the order of the day (a rehearsal, the dump, the import, the schema check, the review, the conversion, the rotation, the start, each node, the validation period, the closing) with the commands
as they exist. `docs/cutover-review.sql` is a set of read-only questions about the imported data (administrators, keys, eggs and their install scripts, schedules and tasks, mounts, nodes, database hosts, settings, activity);
it was run against the test database. Both say plainly what does not exist: the `raptor audit` command (waits for the owner's approval, `docs/plan.md` 13.5), a forced change of passwords, and a real node and bucket.

## Not tested here
A real old database, a real node and a real bucket. Everything above ran against test databases, test buckets and fakes; the rehearsal in `docs/cutover.md` step 1 and the VPS test (`docs/first-test.md`) are where that changes.
