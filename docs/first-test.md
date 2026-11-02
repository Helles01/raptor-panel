# The first test: what it takes, and who does what

Written 2026-11-02. The first test is the "first usable milestone" of `docs/plan.md` (chapter 5): Raptor, the unchanged React frontend, a real
MariaDB and a real Wings on a test machine, with the eight done criteria of the plan. This page says what is needed for it and in which order.

## What exists
All handlers of the first test, written over interfaces and tested with in-memory fakes: sign-in and the checkpoint, the authorizer, the account,
the client reads, power/command/resources/websocket, the whole node API, the pages, the outbox, the admin operations and commands. All 137 routes
of the reference (without `/admin`) are served (`internal/api/inventory`). `scripts/check.sh` is green.

## What is missing, in order
| # | Step | Needs from the user | Size |
|---|---|---|---|
| 1 | The schema: `docs/schema/mysqldump-no-data.sql`, `migrations.txt`, `server-version.txt` (structure only, no data) | the three files, made on the old panel's database | the user's, minutes |
| 2 | `internal/db`: sqlc setup, the baseline, the stores of `docs/store-contract.md` (the first-test group: ~35 interfaces) | – | several chunks |
| 3 | `app.Build` (every handler from its stores, the limits, the health checks), `raptor serve`, the commands registered in `cmd/raptor` | – | 1-2 chunks |
| 4 | A test MariaDB (a local instance, a root password the user sets, an empty database `raptor_test`) | the password, once, typed by the user | the user's |
| 5 | Integration tests against that MariaDB: the conditional updates with two requests at once, the transactions with locks, the host statements (14c) | – | 1-2 chunks |
| 6 | The test machine: WSL2 + Ubuntu LTS + Docker Engine + Wings v1.13.4 | **each install approved with its hash shown first** (WSL2, the Ubuntu image, Docker, the Wings binary) | the user's approvals |
| 7 | The test server while there is no server creation in the flow: `POST /api/application/servers` already exists (M9 over interfaces), so the recommendation stands to create it through the API with an application key, not through a seed command | an `apikey create --application` | – |
| 8 | The eight criteria, each with a log in `docs/modules/milestone-1.md` | the browser (login with TOTP is a human step) | 2-3 chunks |

## The order on the test machine
1. `raptor keygen` for the secrets key (kept in the environment, never in a file that is committed); `raptor db check` on the database, then `raptor db migrate` (Raptor's own additive migration, `raptor_secrets`).
2. `raptor user create --admin --email … --username … --first-name … --last-name … --password-stdin`.
3. `raptor apikey create --user … --application --description … --access servers=rw --access nodes=rw …`; the key is on the standard output, once.
4. `raptor location create`, `raptor node create …` → the `config.yml` for Wings on the standard output; save it on the node.
5. `raptor egg import --nest-id … file.json` after `raptor nest create`.
6. `raptor serve` (behind the test proxy or on `127.0.0.1:8080`), Wings started with the saved `config.yml`; Wings asks `GET /api/remote/servers`.
7. The server is created through the Application API; the install report comes back from Wings (`POST /api/remote/servers/{uuid}/install`).
8. The browser: login, the server list, power, console.

## Open points
- `egg_variables.field_type`: the reference importer passes the file's `field_type`, but no column of that name was found among the migrations; the dump will say.
- The strict CSP of the page shell has not been tried in a browser yet; this is its first test.
- `mysqlSession` (a real connection to a database host) and the `CREATE USER … IDENTIFIED BY ?` / `ALTER USER CURRENT_USER()` statements are covered
  only by fakes until step 5.
- `raptor audit` is written (`internal/audit`) but the command needs the user's separate approval (plan 13.5).
