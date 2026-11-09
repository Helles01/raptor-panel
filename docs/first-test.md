# The first test: what it takes, and who does what

Written 2026-11-02, updated 2026-11-03. The first test is the "first usable milestone" of `docs/plan.md` (chapter 5): Raptor, the unchanged React frontend, a real
MariaDB and a real Wings on a test machine, with the eight done criteria of the plan. This page says what is needed for it and in which order.

## What exists
Everything that runs without a node: the handlers of all 137 routes of the reference, the SQL layer under all of them (tested on a real MariaDB), the
admin commands, `app.Build` and `raptor serve`. `TestPanelEndToEnd` (`cmd/raptor`, build tag `integration`) starts the whole panel on the test database and does what the
milestone does without Wings: the administrator's commands, the sign-in in a browser's way (cookie, anti-CSRF token), two-factor setup and the sign-in with a code, the
server list and page, the Application API with a key, schedules, subusers, the 240 a minute limits, the end of the session and a clean stop. A second test checks that the built
panel serves exactly the routes of the reference (`TestBuiltPanelServesTheReferencesRoutes`). `scripts/check.sh` is green. Notes: `docs/modules/21-build-and-serve.md`.

## What is missing, in order
| # | Step | Needs from the user | State |
|---|---|---|---|
| 1 | The schema | – | done: the reference's own dump (`internal/db/schema/0000_baseline.sql`); the user's dump of their old panel is for the test with real data |
| 2 | `internal/db`: sqlc, the baseline, the stores | – | done |
| 3 | `app.Build`, `raptor serve`, the commands registered | – | done (this page's date) |
| 4 | A test MariaDB | – | done (MariaDB 11.8.9, `docs/testing-database.md`) |
| 5 | Integration tests against that MariaDB | – | done for the stores and the whole panel; the host statements (14c) were checked by `dbhost create`/`test` in the first-installation test |
| 6 | The test machine for a node: Linux + Docker Engine + Wings v1.13.4 | **not on this PC** (decided 2026-11-03): on the user's VPS, when it is ready; each install approved with its hash shown first (`versions.env` has the Wings hash and the Docker key fingerprint) | later, on the VPS |
| 7 | The test server through `POST /api/application/servers` with an application key | an `apikey create --application` | needs the node of step 6 (the panel tells the node after it stores the server, and takes the server back if the node refuses) |
| 8 | The eight criteria, each with a log in `docs/modules/milestone-1.md` | the browser (login with TOTP is a human step) | the ones that need no node can be done on this PC now (login, 2FA, server list from a row, schedules, subusers, settings); power, console, files, backups after 6 and 7 |

## The order on the test machine
0. Before anything is started: `raptor doctor` (it looks at the configuration, the key, the frontend build, the database, the schema, the nodes and the mail, and says what is wrong), and, with S3 settings, `raptor backups check` (it tries the bucket the way a node and a browser would). `deploy/README.md` is how the machine is set up.
1. `raptor keygen` for the secrets key (kept in the environment, never in a file that is committed). An **empty** database: `raptor db init` (the tables of the reference and Raptor's own). A **copy of the old panel's** database: `raptor db check`, then `raptor db migrate` (Raptor's own additive migration, `raptor_secrets`).
2. `raptor user create --admin --email … --username … --first-name … --last-name … --password-stdin`.
3. `raptor apikey create --user … --application --description … --access servers=rw --access nodes=rw …`; the key is on the standard output, once.
4. `raptor location create`, `raptor node create …` → the `config.yml` for Wings on the standard output; save it on the node.
5. `raptor egg import --nest-id … file.json` after `raptor nest create`.
6. `raptor serve` (on `127.0.0.1:8080` or behind the test proxy; it needs `RAPTOR_APP_URL`, `RAPTOR_SECRET_KEY`, `RAPTOR_DB_*`, and the build of the frontend in `web.assets_dir`), with Wings started with the saved `config.yml`; Wings asks `GET /api/remote/servers`.
7. The server is created through the Application API; the install report comes back from Wings (`POST /api/remote/servers/{uuid}/install`).
8. The browser: login, the server list, power, console.

## Open points
- `egg_variables.field_type`: the reference importer passes the file's `field_type`, but no column of that name was found among the migrations; the dump will say.
- The strict CSP of the page shell has been served and checked for its header in `TestPanelEndToEnd`, but no browser has run the frontend against it yet; this is its first test.
- Mail: the panel sends it through the SMTP settings of the `settings` table; the password of the mail server is read from `raptor_secrets`: `raptor settings set-mail-password --password-stdin` sets it on a fresh installation (`docs/modules/24-mail-password.md`), `raptor migrate-secrets` brings the old one.
- `raptor audit` (plan 13.5, `docs/modules/27-audit-and-passwords.md`) checks that the rows of an imported database are usable; it was run on a test database only. Run it on the copy of the old panel's database first (`docs/cutover.md` step 5).
- S3 backups are built and tested against a test bucket (`docs/modules/25-s3-backups.md`), not against a real one: on the VPS, set the `RAPTOR_BACKUPS_*` variables, start a backup from the panel and watch the object appear, then download, restore and delete it. Whether the old panel kept backups on S3 is not known; if it did, its backups are found by the same key.

## The checklist for the VPS (2026-11-09)
Nothing has to be written for the milestone any more: every route, store, command and the deploy files exist and are tested against a real MariaDB and fakes. What is left is a machine, and what a real Wings and a real browser show for
the first time. Neither has ever run against Raptor, so expect rounds of fixing after first contact (the wire formats of the node were written from the reference and its daemon's source, the strict page policy has never met a browser with real data, the console and files have only been tested with fakes).

**Before touching the VPS (on this PC, nothing to install):**
1. `GOOS=linux GOARCH=amd64 go build -o raptor ./cmd/raptor` (the binary), `bash scripts/frontend-check.sh` (the build in `web/public/assets`), and the Wings binary already downloaded (`versions.env` has its SHA-256). Copy the three to the VPS and check the hashes there.
2. Decide the addresses. The simplest first test has **no TLS at all**: `RAPTOR_APP_URL=http://<ip>:8080`, nodes with `--scheme http`. A panel on https needs the node on https too (the browser blocks a console to an http node from an https page), which means two names and two certificates; leave that for the second test. `raptor doctor` will warn about http, which is right.
3. Wings and the panel both default to port 8080: on one machine give the node another one (`raptor node create … --listen-port 8081`, and open 2022 for SFTP).

**On the VPS (Ubuntu 22.04 or 24.04, 2 GB of memory or more):** MariaDB (a new database and user, `deploy/README.md`), Docker Engine (from its own repository, key fingerprint in `versions.env`), then `deploy/README.md` from the top to `raptor doctor`, then "The order on the test machine" above. About one to two hours if nothing goes wrong.

**Then the milestone:** create the location, node, nest and egg (`reference/database/Seeders/eggs/minecraft/egg-paper.json` is a good first egg), start Wings with the saved `config.yml`, create the server with an application key, and go through the eight criteria of the plan in a browser, writing each into `docs/modules/milestone-1.md`.

**What I would expect to break, in this order:** the node's first calls (`GET /api/remote/servers`, the install report), the console (the token and the origin policy), the files, then backups. Each is a small fix with the log of the failing call; keep the panel's log (`journalctl -u raptor`) and the node's (`/var/log/pterodactyl/wings.log`) for every failure.
