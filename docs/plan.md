# Raptor Panel – implementation plan

Status: **version 3, approved (2026-10-22).** M0, M2 and M3b are done, the schema-independent groundwork of M3 too; M1 and M1b are partly done (the schema-independent
parts), see `docs/modules/`. M2 and M3b were done before M1/M1b were finished because they do not need the database
schema. The plan is based on the actual contents of the `reference/` folder; the route counts come from
`reference/routes/*.php`.

Changes since version 2: closed decisions N1–N4 (chapter 3), a "first usable" milestone after M7 (chapter 5), secrets are
rotated at the moment of cutover (N2: A, chapter 13), install onto a clean machine with only the database carried over
from the old machine (13.1), version and hash re-verification before every tool install (chapter 11), the stricter
`execute` check recorded in `docs/compat.md` (C-001).

Changes in version 2 (since version 1): the exact reference version (chapter 1; version 1 wrongly said 1.12.x), new
chapters 10–13 (known bugs, versions and tools, frontend audit, cutover and secret rotation), new modules M1b and M3b,
and the secret migration no longer overwrites the old columns (2.3).

## 1. Goal and scope

A Go backend that exposes the same HTTP API as the reference panel, so that:

- the existing Wings daemon can talk to it without changes (`/api/remote/*`),
- the existing MariaDB/MySQL database can be reused,
- the existing React frontend (`reference/resources/scripts`) stays usable.

**Reference version:** Pterodactyl panel **v1.15.2** (released 2026-10-21), commit
`90b2aa87469da4fffd941b3c99f3c1ca48a01b78`; `reference/` is a shallow clone of the `1.0-develop` branch. Verified: HEAD,
the local tag and the `v1.15.2^{}` tag on `origin` point to the same commit, and according to GitHub this is the latest
stable release (`v2.0.0-beta.*` are pre-releases and not a target). No update was needed. Two warnings:

- `reference/CHANGELOG.md` ends at v1.12.3; the notes for v1.12.4–v1.15.2 exist only on the GitHub release pages.
- `config/app.php` says `canary`; the source of the version is the git tag, not the file.

| Surface | Routes | Used by the frontend? | Plan |
|---|---|---|---|
| `/api/client/*` | 69 | yes (React) | port |
| `/api/application/*` | 42 | no, external clients | port |
| `/api/remote/*` | 12 | no, called by Wings | port |
| `/auth/*`, `/`, `/account`, `/locales/*` | 12 | yes (HTML shell + JSON) | port |
| `/admin/*` | 97 | **not React: server-side Blade HTML** | **not ported** (see 2.1) |

## 2. What the reference taught us, and what shapes the plan

### 2.1 The admin UI cannot be reused
The `/admin/*` pages are Blade templates (`resources/views/admin`); React only provides the login and the user area. As a
consequence, the following exist **only in the admin UI**, not in the Application API:

- creating Application API keys (`/admin/api/new`) – without a key the Application API cannot be used,
- database hosts, mounts, nest/egg creation and import, panel settings (mail, advanced),
- starting a server transfer (the `transfer/success|failure` callbacks from Wings are API, though),
- server mounts, toggling the install flag, allocation alias.

The first release has **no admin UI**. The CLI commands needed for bootstrapping (user, API key, node, location, egg
import, settings) are built **early, in module M1b**, because the panel is unusable without them. An admin UI is a
separate, later decision (M16).

### 2.2 Authentication
- Browser: cookie-based session + CSRF (`XSRF-TOKEN` cookie → `X-XSRF-TOKEN` header; axios sends it automatically).
- API: Sanctum bearer token. A 16 character identifier (`ptlc_` account key, `ptla_` application key) and a 32 character
  secret, stored encrypted in the DB (`api_keys.token`), looked up by identifier.
- Application API: the user must be `root_admin`, plus the key's `r_*` columns (`r_servers`, `r_nodes`, … values: 0 none,
  1 read, 2 write) per resource.
- Client API: owner / admin / subuser (`subusers.permissions` JSON) permission per server (`websocket.connect`,
  `control.*`, `file.*`, `backup.*`, … – the full list is in `app/Models/Permission.php`).
- Wings → panel: `Authorization: Bearer <daemon_token_id>.<token>`; the token is stored encrypted in `nodes.daemon_token`.
- Panel → Wings: `Authorization: Bearer <decrypted node token>`, and the panel **issues JWTs itself** (HS256, signed with
  the node token) for the websocket, file upload/download and backup download. The panel **never receives a JWT**; Wings
  validates the tokens (consequence in 10.1).
- 2FA: TOTP (`users.totp_secret` encrypted) and recovery tokens (`user_recovery_tokens`, bcrypt).
- Rate limits: login/checkpoint 10 requests/minute/IP, password reminder 2/minute, API client/application configurable,
  per-server limits (backup 3/15 min, database 2/min, file pull 5/10 min, subuser 10/15 min, websocket 5/min).

### 2.3 Encrypted values and the migration command
The reference uses Laravel's `Crypt` format (`APP_KEY`, AES-256-CBC + HMAC). The columns are `nodes.daemon_token`,
`api_keys.token`, `users.totp_secret`, `database_hosts.password`, `databases.password`, and the mail password in the
`settings` table.

According to CLAUDE.md this needs a migration command, not a re-implementation inside request handlers. Plan:

- `internal/secrets`: Raptor's own format (AES-256-GCM, `RAPTOR_SECRET_KEY` from the environment, version prefix).
- `raptor migrate-secrets`: decrypts the Laravel format with the old `APP_KEY` and writes the result to a **new place**.
  **It does not overwrite or modify the existing columns**, so there is a way back to the old panel until cutover
  validation ends. It has `--dry-run`, a transaction, is re-runnable (by default it converts only missing values, `--all`
  converts everything again), and only this command contains Laravel decryption code (`internal/legacycrypt`, read-only).
- While running, Raptor **reads only the new place**.
- Neutralising the old columns is a separate, explicitly issued command that asks for confirmation (`--finalize`), only
  after the validation window and the secret rotation (chapter 13).
- Risk to test explicitly in the module: Laravel's `encrypt()` encrypts a PHP-`serialize`d value by default,
  `encryptString()` does not; for each column it must be verified which format is stored.

**The "new place" question (closed, N1: a new `raptor_secrets` table).** New columns were considered first. The old
panel's models were checked: the `User` `$hidden` list is `['password','remember_token','totp_secret','totp_authenticated_at']`,
`Node`'s is `['daemon_token_id','daemon_token']`, `ApiKey`'s is `['token']`, `Database`/`DatabaseHost`'s is `['password']`.
A new column (e.g. `users.totp_secret_raptor`) is **not on those lists**, so the old panel's `toArray()`-based output
would expose it; for users that is `window.PterodactylUser` (`toVueObject()` → `toArray()`), meaning the value encrypted
by Raptor would reach the browser. A **new, separate `raptor_secrets` table** (`table_name`, `row_id`, `column_name`,
`ciphertext`, `source_hash`, timestamps) gives the same way back (old columns untouched) and the same additive nature,
but does not touch the existing tables and has no leak risk. This became the decision (N1).

**Limits of the way back:**
- Secrets Raptor creates after cutover (new API key, new node token, new 2FA setup) are invisible to the old panel. For
  new rows Raptor writes an invalid placeholder into the old columns (they may be `NOT NULL`), so the old panel cannot do
  anything with them.
- Password hashes stay compatible: Raptor writes `$2a$` hashes, which PHP's `password_verify` accepts.
- Secret rotation is part of the cutover (N2: A), which makes the way back harder (13.4).

### 2.4 Other compatibility points
- Password hash: normalise the `$2y$` → `$2a$` prefix before bcrypt verification (per CLAUDE.md).
- Response format (Fractal): `{"object":"server","attributes":{…},"relationships":{…}}`, for lists
  `{"object":"list","data":[…],"meta":{"pagination":{…}}}`, for errors `{"errors":[{"code","status","detail"}]}`. The React
  client relies on these, so key names and null handling must follow `app/Transformers/**` exactly.
- Timestamps are RFC 3339, UTC (`APP_TIMEZONE`, UTC by default).
- Hashids (`HASHIDS_SALT`, length 8): the database route binding is hash based in the client API; the use for the other
  models (Allocation, Schedule, Subuser) is to be checked at the start of the module.
- Route bindings in the client API: `{server}` is looked up by `uuidShort` when the value has 8 characters, otherwise by
  `uuid`; `{user}` (subuser) by the user's `uuid`.
- Schema version: the last migration in v1.15.2 is `2024_07_13_091852_clear_unused_allocation_notes`. At startup
  (`raptor db check`, read-only) Raptor compares the `migrations` table with a built-in list and refuses to start on a
  difference.
- The session cookie is an encrypted Laravel cookie; old sessions cannot be carried over, **everybody is logged out at
  the moment of cutover**. The `sessions` table (id, user_id, ip_address, user_agent, payload, last_activity) can be used
  without DDL changes; the `payload` will be Go-side JSON.
- The frontend comes as a `main.js` webpack bundle; the Blade wrapper injects `window.PterodactylUser`,
  `window.SiteConfiguration` and the csrf meta tag. In Go, an `html/template` shell does the same (for escaping of
  user-supplied values see 10.2). Build: Yarn classic + `yarn.lock`, Node ≥ 22, React 16, easy-peasy, axios, Tailwind 3.
- Websocket: console/statistics go directly between the browser and Wings; the panel only has to issue the JWT. There is
  no websocket server in the panel.
- Background work: the Laravel scheduler runs server schedules every minute (`RunTaskJob`); depending on configuration it
  prunes orphaned backups every 30 minutes (`backups.prune_age`) and the activity log daily (`activity.prune_days`);
  e-mail notifications go through the queue (AccountCreated, AddedToServer, RemovedFromServer, SendPasswordReset,
  ServerInstalled); SFTP/websocket access is revoked by `RevokeSftpAccessJob`. In Go there will be an in-process
  scheduler, an SMTP sender and a durable "outbox", without Redis and a separate queue worker.
- Development environment: Go, MariaDB/MySQL, Docker, Node, Yarn and sqlc were **not installed** on this machine at the
  start. Wings needs Linux (on Windows, WSL2 is needed for the integration test). Install list: chapter 11.

## 3. Decisions

| # | Question | Status | Notes |
|---|---|---|---|
| 1 | HTTP framework | **Echo – approved** | The handler returns an `error`, a single central error handler produces the `{"errors":[…]}` format. |
| 2 | Encrypted columns | **One-way migration, old columns untouched – approved** | The new place: the `raptor_secrets` table (N1). |
| 3 | Admin UI | **None in the first release – approved** | CLI brought forward (M1b). |
| 4 | Schema for sqlc | **The project owner provides the `mysqldump --no-data` output** | Goes into `docs/schema/`, without data (see below). **Not there yet** (as of 2026-10-22). |
| 5 | Wings test environment | **WSL2 + real Wings – approved** | The Wings version is pinned (chapter 11). |
| 6 | Extending the layout | **approved** | Chapter 4. |
| 7 | Module order | **approved** | M1b and M3b are new, numbering unchanged (chapter 5). |
| 8 | Tool installation | **approved, official sources only** | List and rules in chapter 11; before every install the version and hash are re-verified on the official site and shown to the owner. |

**Location of the schema files (decision 4).** Please put them here; the owner creates the folder (the folder is part of
the plan):

- `docs/schema/mysqldump-no-data.sql`: the output of `mysqldump --no-data` (structure only, no data).
- `docs/schema/migrations.txt`: the `migration` column of the `migrations` table (for the schema version check, because a
  `--no-data` dump does not contain rows).
- `docs/schema/server-version.txt`: the database server version (`SELECT VERSION();`).

The dump must not contain data or credentials; the files are read from there and turned into `internal/db/schema/` (the
sqlc input). If the dump differs from the final state of the v1.15.2 migrations, the difference is reported.

**Closed decisions (2026-10-22):**

| # | Question | Decision |
|---|---|---|
| N1 | Where are Raptor-format secrets stored? | **A new `raptor_secrets` table** (2.3) |
| N2 | When are secrets rotated? | **A: at the moment of cutover**; a leak of the `APP_KEY` is assumed (13.3) |
| N3 | Should `POST …/schedules/{id}/execute` also check the action permission of the tasks? | **Yes** (10.1 b); stricter than the reference, recorded as C-001 in `docs/compat.md` |
| N4 | May sources not on the original list be installed? | **sqlc, govulncheck, Yarn: yes. WSL2, Docker, Wings: only before M7** (11.2) |

## 4. Package layout

The layout of CLAUDE.md remains the base; items marked **(new)** are the approved extensions.

```
cmd/raptor/                 serve + CLI subcommands (user, apikey, node, location, egg, settings, migrate-secrets, db, …)
internal/
  config/                   env + ignored config file loading
  db/                       sqlc output, queries/*.sql, schema/ (baseline), additive migrations
  auth/                     password (bcrypt $2y), session, TOTP, API key, permission resolution
  wings/                    client for the node daemon + JWT issuing (mandatory scope)
  api/
    router.go               route registration, mandatory permission descriptor
    middleware/             session, CSRF, API key, throttle, security headers, logging
    apierr/  resp/          error format, Fractal-like envelopes, pagination
    validate/               allow-list validation helpers
    client/ application/ remote/ authapi/ web/
  secrets/                  (new) Raptor encryption format
  legacycrypt/              (new) Laravel format, used only by migrate-secrets
  hashid/                   (new) Hashids-compatible identifiers
  activity/                 (new) activity log writing (server:*, auth:*, user:*), fields from an allow-list
  mail/                     (new) SMTP + templates
  outbox/                   (new) durable retry (SFTP/JWT revocation on Wings)
  servers/ nodes/ backups/ schedules/ dbhosts/   (new) domain logic, only where there is real logic
web/                        the frontend (copy of reference/resources/scripts + build)
docs/                       plan.md, modules/NN-*.md, compat.md, frontend-audit.md, schema/
versions.env                (new) pinned versions (Wings, Go, Node, MariaDB, sqlc)
```

Dependency direction: `api` → domain packages → `db` / `wings` / `secrets`. A simple CRUD handler calls `db` directly
through a small interface; a separate domain layer exists only where there is logic (server creation, Wings config
assembly, backup state machine, scheduler, DB host provisioning, permission resolution). No global state,
`context.Context` is the first parameter, `cmd/raptor` wires the dependencies together.

Database rules:
- Raptor never changes the structure of existing tables or the content of existing columns (exceptions: an operation
  issued by a user, such as a password change, and `--finalize` as described in chapter 13), and never touches the Laravel
  `migrations` table.
- New tables can only be created by an additive migration, tracked in Raptor's own `raptor_migrations` table.
- sqlc: MySQL engine, `emit_interface: true` (so handler tests can run with a fake `Querier`), `WithTx` for transactions.
- Dynamic SQL is allowed only when creating databases/users on external DB hosts, with an identifier allow-list and
  quoting.

## 5. Modules and order

Every module: implementation → tests → a short summary in `docs/modules/NN-<name>.md` of **what matches the reference**
and **what differs on purpose**; the differences are also recorded in `docs/compat.md`. The next module starts after
approval. The "10.x" markers refer to the known-bugs chapter: those rules and tests are a mandatory part of the module.

| # | Module | Content | Packages |
|---|---|---|---|
| M0 | Scaffold | `go.mod`, `NOTICE` (MIT copyright text from the reference, missing so far), gofmt/vet/test script, `versions.env`, CI steps (`govulncheck`, chapter 12 audit) | – |
| M1 | Config, DB, sqlc, secrets | env/config loading, DB connection, baseline schema from the `docs/schema/` files, `raptor db check`, sqlc setup, first queries (users, nodes, servers), `internal/secrets`, the `raptor_secrets` table, `raptor migrate-secrets` (old columns untouched) | `config`, `db`, `secrets`, `legacycrypt`, `cmd/raptor` |
| **M1b** | **Admin CLI (brought forward)** | `raptor user create/list/delete/disable-2fa`, **`raptor apikey create/list/revoke`** (application and account keys, with `r_*` levels), **`raptor egg import`** (+ `nest list/create`; based on the reference `EggImporterService` and `database/Seeders/eggs`), `raptor location create/list/delete`, `raptor node create/list/configuration` (Wings `config.yml` output), `raptor settings get/set`. DB and `secrets` only, no HTTP. The egg import works from a file and does not download from a URL. | `cmd/raptor`, `nodes`, `auth`, `secrets` |
| M2 | HTTP foundation | router + mandatory permission descriptor, error format, envelopes, pagination, validation helpers, throttle (the key is never constant, 10.2), trusted proxy list, security headers, logging with secret filtering | `api`, `api/middleware`, `apierr`, `resp`, `validate` |
| M3 | Login and session | `POST /auth/login`, `/login/checkpoint` (TOTP + recovery, replay protection), `/logout`, password reminder and reset, session store, CSRF, `ptlc_` key authentication, 2FA requirement, HTML shell (`PterodactylUser`, `SiteConfiguration` injected safely), `/locales/locale.json` from a fixed allow-list (10.1 c) | `auth`, `api/authapi`, `api/web`, `mail` |
| **M3b** | **Frontend build and audit** | `web/` as a copy of `reference/resources/scripts` + build config from v1.15.2, `yarn install --frozen-lockfile`, `tsc`, `lint`, `build:production`, dependency audit and `docs/frontend-audit.md` (chapter 12), static serving | `web/`, `api/web` |
| M4 | Account API | `/api/client/account*`: details, email (throttle), password (+SFTP revocation), 2FA, API keys (locked limit), SSH keys, account activity | `api/client`, `auth`, `activity` |
| M5 | Server list and details (read-only) | client: `GET /`, `/permissions`, `/servers/{server}`, `/servers/{server}/activity` (hide foreign IPs); application: GET users, nodes (+`allocations`), locations, servers, nests, eggs; permission resolution (owner/admin/subuser, `r_*` key permissions), server state checks (suspended, installing, transferring, node maintenance); every resource below `{server}` is loaded scoped to the server (10.2) | `api/client`, `api/application`, `auth`, `servers` |
| M6 | Power and Wings client | `POST /power`, `/command`, `GET /resources`, `GET /websocket` (JWT); Wings HTTP client (timeouts, TLS, error mapping); JWT issuing **with a mandatory scope and a check of the permission belonging to the scope** (10.1 a) | `wings`, `api/client` |
| M7 | Daemon API | the non-backup part of `/api/remote/*`: sftp auth, servers list/reset, server details (`settings` + `process_configuration`), install script and result, transfer success/failure, activity processing. **Node scope enforced structurally** (10.1 e). **Tested with the pinned Wings** (chapter 11). | `api/remote`, `servers` |
| M8 | Users, nodes, locations, allocations | application API writes: users, nodes (+`/configuration` for the Wings `config.yml`), locations, allocations | `api/application`, `nodes` |
| M9 | Server lifecycle (Application API) | create (install + deployment: node/allocation selection, variable validation), details (on owner change the old owner's access is revoked), build, startup, suspend, unsuspend, reinstall, delete (+force), `/nodes/deployable` | `servers`, `nodes`, `wings` |
| M10 | Subusers and permissions | `/servers/{server}/users*`, permission set validation (registered permissions only), notification e-mail, JWT and SFTP revocation on Wings through a durable outbox (10.2) | `api/client`, `servers`, `mail`, `outbox` |
| M11 | Server self-service | startup (read, variable update), settings (rename, reinstall, docker-image), network allocations (locked limit, 10.1 d) | `api/client`, `servers` |
| M12 | Schedules | schedules + tasks CRUD, `execute`, in-process scheduler (every minute, cron expression, `only_when_online`, `continue_on_failure`); **per-action permission** (10.1 b) | `schedules`, `api/client` |
| M13 | Backups | client backup endpoints, download JWT, lock, restore; remote: `/api/remote/backups/*`; orphaned backup pruning; the backup identifier is generated by the panel and format-checked; limit with locking (10.1 d). **Tested with the pinned Wings.** | `backups`, `api/client`, `api/remote` |
| M14 | Databases | client and application server-database endpoints, `database_hosts` management (CLI: `raptor dbhost`), creating DBs and users on the host, password rotation, hashid, limit with locking (10.1 d) | `dbhosts`, `hashid`, `api/*` |
| M15 | File manager | `/files/*` (list, contents, download, rename, copy, write, compress, decompress, delete, create-folder, chmod, pull, upload JWT), scheme check of the `pull` URL | `wings`, `api/client` |
| M16 | Admin UI | **not planned**, separate decision (2.1) | – |
| M17 | Cutover | packaging (Dockerfile), cutover and rollback description, **secret rotation** (chapter 13), `--finalize`, end-to-end test with real Wings (pinned version) + unchanged React frontend, negative JWT and node-scope tests | – |

M6 and M7 are to be tested together with real Wings (on startup Wings requests the server list from the panel).

### First usable milestone (after M7)

After M7 is closed there is a separate milestone: Raptor can be tried end to end on a **test machine**. Its prerequisites
are M0–M7 plus M1b (CLI) and M3b (frontend build). Not part of it: server creation (M9), subusers (M10), schedules,
backups, databases, file manager.

Test machine: WSL2 + Ubuntu LTS + Docker Engine + the pinned **Wings v1.13.4**, with a clean Raptor install and a clean
MariaDB (the model of 13.1 in miniature). WSL2, Docker and Wings are installed only before M7 starts, as a separate step.

Done criteria (each proven in `docs/modules/milestone-1.md` with a test log):
1. Raptor starts on a clean DB; `raptor db check` is green; `raptor user create --admin`, `node create`, `egg import` work.
2. **Login**: on the unchanged React frontend, login with password and TOTP, and logout.
3. **Server list and details**: the user sees their own servers and not foreign ones.
4. **Wings connection**: Wings requests the server list from Raptor and starts; the node is connected and there is no
   error in the Wings log.
5. **Power**: start, stop, restart, kill; Wings actually changes the container state; an activity log entry is written.
6. **Console**: websocket JWT issuing, the browser connects to Wings directly, output arrives, a command can be sent.
7. The negative tests are green: rejection of a wrongly scoped JWT (10.1 a) and the node-scope matrix (10.1 e) on real Wings.
8. `scripts/check.sh` is green (gofmt, vet, tests, govulncheck, frontend audit).

Open question, to be decided at the end of M7: where does the test server come from while there is no server creation
(M9)? (a) a copy of the old DB with the nodes rewritten, or (b) a test-only `raptor dev seed-server` command (build tag
`dev`, not part of the release build). Recommendation: (b), so that no production data reaches the test machine.

## 6. Route → Go package mapping

Notation: **permission** = the mandatory permission descriptor of the route (the router does not allow registering a
route without one).

### 6.1 Client API (`/api/client`) → `internal/api/client`
Permission on every route: a logged-in user (session or `ptlc_` key); for routes below `{server}` additionally server
access + the subuser permission given below. An application key (`ptla_`) is forbidden here. The permission column was
derived from the reference routes and the `Permission` constants; the final mapping (especially the permission by
signal for `files/*` and `power`) is refined at the start of each module from the `app/Http/Requests/Api/Client/**`
classes.

| Route | File | Subuser permission | Module |
|---|---|---|---|
| `GET /` , `GET /permissions` | `servers.go`, `permissions.go` | – | M5 |
| `GET /account`, `PUT /account/email`, `PUT /account/password` | `account.go` | – | M4 |
| `GET /account/two-factor`, `POST /account/two-factor`, `POST /account/two-factor/disable` | `twofactor.go` | – | M4 |
| `GET /account/activity` | `activity.go` | – | M4 |
| `GET/POST /account/api-keys`, `DELETE /account/api-keys/{identifier}` | `apikeys.go` | – | M4 |
| `GET/POST /account/ssh-keys`, `POST /account/ssh-keys/remove` | `sshkeys.go` | – | M4 |
| `GET /servers/{server}` | `servers.go` | – | M5 |
| `GET /servers/{server}/activity` | `activity.go` | `activity.read` | M5 |
| `GET /servers/{server}/websocket` | `websocket.go` | `websocket.connect` | M6 |
| `GET /servers/{server}/resources` | `resources.go` | – | M6 |
| `POST /servers/{server}/command` | `command.go` | `control.console` | M6 |
| `POST /servers/{server}/power` | `power.go` | `control.start|stop|restart` (by signal) | M6 |
| `GET /servers/{server}/startup`, `PUT …/startup/variable` | `startup.go` | `startup.read`, `startup.update` | M11 |
| `POST …/settings/rename`, `POST …/settings/reinstall`, `PUT …/settings/docker-image` | `settings.go` | `settings.rename`, `settings.reinstall`, `startup.docker-image` | M11 |
| `GET/POST …/network/allocations`, `POST …/allocations/{id}`, `POST …/{id}/primary`, `DELETE …/{id}` | `network.go` | `allocation.read|create|update|delete` | M11 |
| `GET/POST …/users`, `GET/POST/DELETE …/users/{user}` | `subusers.go` | `user.read|create|update|delete` | M10 |
| `GET/POST …/schedules`, `GET/POST/DELETE …/schedules/{schedule}`, `POST …/{schedule}/execute` | `schedules.go` | `schedule.read|create|update|delete` (+ the per-action permission of the tasks at `execute`, N3) | M12 |
| `POST …/schedules/{schedule}/tasks`, `POST/DELETE …/tasks/{task}` | `scheduletasks.go` | `schedule.update` + the permission of the action (`control.console`, `backup.create`, `control.start|stop|restart`) | M12 |
| `GET/POST …/backups`, `GET …/{backup}`, `GET …/{backup}/download`, `POST …/{backup}/lock`, `POST …/{backup}/restore`, `DELETE …/{backup}` | `backups.go` | `backup.read|create|download|delete|restore` | M13 |
| `GET/POST …/databases`, `POST …/{database}/rotate-password`, `DELETE …/{database}` | `databases.go` | `database.read|create|update|delete` (+`view_password`) | M14 |
| `GET …/files/list`, `…/contents`, `…/download`, `GET …/files/upload` | `files.go` | `file.read`, `file.read-content`, `file.create` | M15 |
| `PUT …/files/rename`, `POST …/copy|write|compress|decompress|delete|create-folder|chmod|pull` | `files.go` | `file.update|create|archive|delete` | M15 |

### 6.2 Application API (`/api/application`) → `internal/api/application`
Permission: a `root_admin` user, key type `ptla_`, and the key's `r_<resource>` level (read or write).

| Route | File | Resource | Module |
|---|---|---|---|
| `GET /users`, `/users/{id}`, `/users/external/{external_id}` | `users.go` | `users` | M5 |
| `POST /users`, `PATCH /users/{id}`, `DELETE /users/{id}` | `users.go` | `users` (write) | M8 |
| `GET /nodes`, `/nodes/{id}`, `/nodes/{id}/allocations` | `nodes.go`, `allocations.go` | `nodes`, `allocations` | M5 |
| `GET /nodes/{id}/configuration`, `POST /nodes`, `PATCH/DELETE /nodes/{id}` | `nodes.go` | `nodes` (write) | M8 |
| `POST /nodes/{id}/allocations`, `DELETE …/{allocation}` | `allocations.go` | `allocations` (write) | M8 |
| `GET /nodes/deployable` | `nodes.go` | `nodes` | M9 |
| `GET /locations*` | `locations.go` | `locations` | M5 |
| `POST/PATCH/DELETE /locations*` | `locations.go` | `locations` (write) | M8 |
| `GET /servers`, `/servers/{id}`, `/servers/external/{external_id}` | `servers.go` | `servers` | M5 |
| `POST /servers`, `PATCH …/details|build|startup`, `POST …/suspend|unsuspend|reinstall`, `DELETE …/{id}[/{force}]` | `servers.go`, `serverdetails.go`, `servermgmt.go` | `servers` (write) | M9 |
| `GET /servers/{id}/databases[/{database}]` | `serverdatabases.go` | `server_databases` | M14 |
| `POST …/databases`, `POST …/{database}/reset-password`, `DELETE …/{database}` | `serverdatabases.go` | `server_databases` (write) | M14 |
| `GET /nests`, `/nests/{id}`, `/nests/{id}/eggs`, `/nests/{id}/eggs/{egg}` | `nests.go`, `eggs.go` | `nests`, `eggs` | M5 |

### 6.3 Daemon API (`/api/remote`) → `internal/api/remote`
Permission: node token (`Bearer <token_id>.<token>`), with **structural** enforcement on every route that the requested
server/backup belongs to the calling node (10.1 e); during a transfer both the old and the new node may read the server's
data.

| Route | File | Module |
|---|---|---|
| `POST /sftp/auth` | `sftp.go` | M7 |
| `GET /servers`, `POST /servers/reset`, `POST /activity` | `servers.go`, `activity.go` | M7 |
| `GET /servers/{uuid}`, `GET/POST /servers/{uuid}/install` | `servers.go`, `install.go` | M7 |
| `POST /servers/{uuid}/transfer/failure`, `…/success` | `transfer.go` | M7 |
| `GET /backups/{backup}`, `POST /backups/{backup}`, `POST /backups/{backup}/restore` | `backups.go` | M13 |

### 6.4 Auth and base pages → `internal/api/authapi`, `internal/api/web`
| Route | Package | Permission | Module |
|---|---|---|---|
| `GET /auth/login`, `/auth/password`, `/auth/password/reset/{token}`, fallback | `web` | guest | M3 |
| `POST /auth/login`, `POST /auth/login/checkpoint` | `authapi` | guest, throttle 10/minute, key IP + identifier (10.2) | M3 |
| `POST /auth/password`, `POST /auth/password/reset` | `authapi` | guest, throttle 2/minute/IP | M3 |
| `POST /auth/logout` | `authapi` | logged in, CSRF | M3 |
| `GET /`, `GET /account`, `GET /{react}` | `web` | logged in | M3 |
| `GET /locales/locale.json` | `web` | public, fixed allow-list, throttle | M3 |

### 6.5 Admin (`/admin/*`, 97 routes) – not ported, replaced by CLI/API
| Admin area | Replacement in the first release |
|---|---|
| users, locations, nodes (+allocations) | CLI in M1b, Application API in M8 |
| servers: creation, build, startup, suspend, reinstall, deletion, databases | Application API (M9, M14) |
| API keys (`/admin/api`) | `raptor apikey create/list/revoke` (**M1b**) |
| nests, eggs (import) | `raptor egg import`, `raptor nest …` (**M1b**); the eggs already in the DB stay |
| editing eggs, variables, scripts, export | none (M16) |
| panel settings (general, mail, advanced) | config file / env, `raptor settings get/set` (**M1b**) |
| database hosts | `raptor dbhost …` (M14) |
| mounts, server mounts | none (M16) |
| starting a server transfer | none (M16); the callbacks from Wings (M7) exist |

## 7. Cross-cutting rules

- **Permission on every route**: the router only allows registering a route with a permission descriptor (`Public`,
  `Authenticated`, `Admin`, `ApplicationKey{res,level}`, `ServerPerm("control.start")`, `Daemon`); a test walks over all
  routes and fails if one is missing.
- **Validation on every endpoint**: simple allow-lists (character set, length, enumerated values); the rules are read out
  of the reference `app/Http/Requests/**` files; a table-driven test per endpoint for bad input. A value coming from a
  request **never forms a file path** (10.1 c).
- **SQL**: parameterised queries only (sqlc), except the DB host provisioning described in chapter 4.
- **Secrets**: passwords, tokens, API keys and TOTP secrets never appear in logs or activity log properties; the logger
  filters headers and fields, and a test guards this (10.2).
- **Errors**: wrapped with context (`fmt.Errorf("…: %w", err)`); a message sent to the client does not leak internals.
- **Code**: gofmt, go vet, table-driven tests, no global variables, `context.Context` as the first parameter.
- **Known bugs**: every row of chapter 10 is a done-criterion of its module (rule + test).
- **Licence**: for parts adapted from the reference the copyright text goes into `NOTICE`; the code is not a line by line
  translation.

## 8. Test strategy

1. Unit tests: table-driven, with a fake `Querier` and a fake Wings client; handler tests with `httptest`.
2. Contract tests: response key sets and null handling checked against JSON fixtures derived from `app/Transformers/**`.
3. Integration tests (build tag `integration`): real MariaDB with the baseline schema; the `RAPTOR_TEST_DSN` environment
   variable; the concurrency tests (10.1 d) run here. Matrix: MariaDB 10.11 and 11.8 (chapter 11).
4. Encryption: Laravel-format test vectors for `migrate-secrets` (serialized and non-serialized variants), and a test that
   the command leaves the old columns byte-identical.
5. Permission matrix tests: node × route (10.1 e), user × server × resource (10.2), task action × permission (10.1 b).
6. End to end (M7, M13, M17): against real **Wings of the version pinned in `versions.env`** + the unchanged React
   frontend. At startup the test checks that the running Wings version matches the pinned one, and stops otherwise.
7. Security regression tests: at least one test per row of chapter 10 that would reproduce the original bug.

## 9. Non-goals (for now)

reCAPTCHA, telemetry, the `upgrade` command, maintenance mode, Redis/queue worker, a Blade admin UI, and the novelties of
the Pterodactyl v2 branch (`2.0-develop`). The target is v1.15.2 behaviour.

## 10. Known bugs and fixes

Goal: do not repeat in Raptor the bugs that were already fixed in the reference. Every entry: what happened → what the
reference v1.15.2 does → Raptor's rule and test → which module.

**Method and limits.** Sources: `reference/CHANGELOG.md` (up to v1.12.3), the GitHub release pages of `pterodactyl/panel`
and `pterodactyl/wings` (v1.13.0–v1.15.2, and Wings v1.13.x), and the public security advisories of both repositories
(state of 2026-10-22). The text of the advisories and release notes was summarised by a fetch tool and **not all of it
was read word for word**, so the summaries here are secondary sources. At the start of each affected module the original
advisory (GHSA identifier) must be read and the rule adjusted to it. The oldest part of the Wings advisory list (before
2021 and the first 2023 entries) was only partly visible. The GitHub notes for v1.13.0–v1.15.1 are mostly PR lists; the
notes for v1.14.0, v1.14.1 and Wings v1.13.1/v1.13.2 are empty; the content of the v1.13.0 "API key overflow" fix has to
be looked up in the PR (#5563) before M4.

### 10.1 The five highlighted items

**a) Binding JWTs to a purpose** – GHSA-8r6w-3qq5-4p4r (CVE-2026-54593, High; Panel <1.12.3, Wings <1.12.2).
- What happened: Wings accepted any panel-signed JWT that contained the `server_uuid`, `user_uuid` and `unique_id`
  fields, with no purpose. A websocket token issued with the `websocket.connect` permission therefore also worked on the
  `/upload/file` endpoint, so files could be uploaded without `file.create`.
- Reference v1.15.2: every JWT gets a `scope` claim (`JwtScope`: `websocket`, `file-upload`, `file-download`,
  `backup-download`, `transfer`); generating a token without a scope throws (v1.12.3). `aud` is the node's connection
  address, `jti` is a hashed identifier, and there are `unique_id`, `user_uuid` and an expiry.
- Raptor rule: the JWT issuing function of the `wings` package takes a **mandatory scope parameter** (there is no token
  without a scope) and checks the permission belonging to the scope **at issue time**: `file-upload` → `file.create`,
  `file-download` → `file.read-content`, `websocket` → `websocket.connect`, `backup-download` → `backup.download`. Short
  expiry. `scope` is the purpose claim; `aud` stays the node address, as in the reference (compatibility), because the
  purpose binding is provided by `scope`.
- Important clarification: the panel **does not receive** JWTs; the "receiving side" is Wings. Raptor cannot enforce
  that, only the issuing side. Therefore (1) Wings ≥ 1.12.2 is required, the pinned version is 1.13.4, and (2) M7 and M17
  get negative tests on real Wings: the upload and download endpoints reject a websocket token, the websocket rejects an
  upload token, and so on.
- Test: a unit test of the issuing function (it does not compile or fails without a scope; it issues no token without the
  permission), and an e2e test with Wings.
- Modules: M6 (issuing), M13, M15, M7 and M17 (e2e).

**b) Per-action permission of scheduled tasks** – no advisory found for it; the requirement is right.
- Reference v1.15.2: besides `schedule.update`, `StoreTaskRequest::authorize` checks the permission of the action
  (`Task::permissionForAction`: `command` → `control.console`, `backup` → `backup.create`, `power`: `start` →
  `control.start`, `stop` and `kill` → `control.stop`, `restart` → `control.restart`), and does so both when creating and
  when updating a task. `POST …/schedules/{id}/execute` (`TriggerScheduleRequest`), however, **asks only for
  `schedule.update`**, so a subuser without `control.*` or `backup.create` can start a schedule that contains such a task.
  When it runs, the scheduler acts as the system and does not store the creator.
- Raptor rule: (1) creating and updating a task: `schedule.update` **and** the permission of the action; (2) an unknown
  action or payload → denied (the default is deny); (3) `execute`: also check the action permission of every task of the
  schedule (**stricter than the reference, a deliberate difference, N3 – approved, recorded as `docs/compat.md` C-001**).
  Deleting a task stays with `schedule.update`.
- Test: a table-driven test with an action × payload × subuser-permission matrix, separately for `execute`.
- Module: M12.

**c) `/locales/locale.json`** – GHSA-24wv-6c99-f843 (CVE-2025-49132, **Critical**, unauthenticated RCE; Panel <1.11.11).
- Reference v1.15.2: the route is public (without `auth` and 2FA middleware), `LocaleRequest` filters with regexes
  (`locale`: `^[a-z][a-z]$`, `namespace`: `^[a-z]{1,191}$`) and then hands over to Laravel's translation loader.
- Raptor rule: the permitted `locale` and `namespace` values are a **fixed allow-list** built at startup from the
  translation files embedded at build time (`embed.FS`); the request value is only compared with the keys of that list,
  **never forms a file path, and no file is opened based on the request**. An unknown value → 404/422. Throttle.
- Test: `../`, `%2e%2e%2f`, `\`, a NUL byte, upper case, a too long value, empty, an array-typed parameter, known good pairs.
- Module: M3.

**d) Database limit (and other resource limit) checks** – GHSA-jw2v-cq5x-q68g (CVE-2025-69198, Medium, <1.12.0) and
GHSA-fgmm-w5cx-vrfw (CVE-2026-35202, Low, a misconfigured lock, fixed in 1.12.3).
- Reference v1.15.2: `lockForUpdate()->count()` inside a transaction for backups, databases, allocations and account API
  keys (25 of them).
- Raptor rule: in one transaction **lock the parent row first** (`SELECT … FOR UPDATE` on the `servers` or `users` row),
  then count and insert in the same transaction. This way counting and inserting are not separate steps, and there is no
  gap for parallel requests even on an empty set. It covers `database_limit`, `backup_limit`, `allocation_limit` and the
  account API key limit.
- Test (integration, MariaDB): N parallel requests against the limit; the limit can never be exceeded; an implementation
  without the lock fails the test.
- Modules: M4 (API key), M11 (allocation), M13 (backup), M14 (database).

**e) Scope of a Wings node token** – GHSA-g7vw-f8p5-c728 (CVE-2026-26016, **Critical**; Panel ≤1.12.0 → 1.12.1).
- What happened: `GET /api/remote/servers/{uuid}`, `ServerTransferController` (`failure`, `success`) and
  `ServerInstallController` found a server by uuid but did not check that the requesting node is the one the server
  belongs to. A node could fetch the configuration of any other node's server (and the egg install script, with secrets
  in it), and a false "transfer success" could cause the server to be deleted on the source node.
- Reference v1.15.2: every remote controller has an explicit node check (`ServerDetailsController`,
  `ServerInstallController`, `ServerTransferController`, `BackupStatusController`, `BackupRemoteUploadController`,
  the `node_id` filter in `SftpAuthenticationController`, `$node->servers()` in `ActivityProcessingController`).
- Raptor rule: **structural protection** against a forgotten check: the `api/remote` handlers receive only a `NodeScope`
  store whose every query must take a `nodeID` parameter (`… WHERE node_id = ?`); an unscoped server query cannot be
  reached from there (an architecture test on the package's imports and the store interface). Exception: during a
  transfer both the old and the new node may read the server's details; who may call `success`/`failure` is specified
  as in the reference, refined at the start of M7.
- Test: two nodes × every `/api/remote` route × own and foreign server/backup identifier; a foreign identifier is always
  rejected (with the same response for a foreign and a non-existent identifier, so it cannot be probed). Constant-time
  comparison when authenticating by node token.
- Modules: M7, M13 (backup remote), M17 (e2e).
- Related Wings advisory: GHSA-gqmf-jqgv-v8fw (CVE-2024-34066): arbitrary file read/write if a node token leaks. That is
  why protecting and rotating the node token matters (chapter 13).

### 10.2 Further items affecting Raptor

| Source | Bug | Raptor rule and test | Module |
|---|---|---|---|
| GHSA-xvc3-826v-xf47 (CVE-2026-61609, High; 1.7.0–1.12.4 → 1.13.0) | The key of the `throttle:authentication` limiter was empty, so all requests fell into one shared bucket: a single IP with about 10 requests per minute could lock everybody (admins too) out of login and the 2FA check. In the v1.15.2 code the key is the IP. | The throttle key is never constant: login = IP + identifier, checkpoint = IP + confirmation token, password reminder = IP. The client IP is taken from `X-Forwarded-For` only for the trusted proxies list (`RAPTOR_TRUSTED_PROXIES`); behind a proxy without that setting, a warning is logged at startup. Test: exhausting IP A does not block IP B; a regression test that fails for a constant key. | M2, M3 |
| GHSA-rgmp-4873-r683 (CVE-2025-69197, Medium) | A TOTP code could be reused within its validity window. | Based on `users.totp_authenticated_at`, a code of the same or an older time window is rejected (the reference does this). Test: the same code twice. | M3 |
| GHSA-5vfx-8w6m-h3v4 (CVE-2021-41129, High) and CHANGELOG | A 2FA confirmation token flaw, in rare cases an authentication bypass; earlier the 2FA code was asked for before the password was checked (a user-existence leak). | The password is checked before 2FA; the confirmation token is 64 random characters, bound to the session and the user, single use, 5 minutes, compared in constant time; the checkpoint is throttled. Test: expired, reused, another user's token. | M3 |
| GHSA-c479-wq8g-57hr (CVE-2024-49762, Medium) | The password was logged as plain text when 2FA was disabled. | The properties of activity log entries come from a per-event allow-list; request input does not go in. Test: search the logger and the activity output for password-, token-, secret- and recovery-like fields on every affected endpoint. | M2, M4 |
| GHSA-wwgq-9jhf-qgw6, GHSA-m49f-hcxp-6hm6 (CSRF: test e-mail, node token generation, forced logout) | Missing CSRF protection on state-changing requests. | Every session-authenticated request with an unsafe method is CSRF-checked (logout is POST); bearer-key requests and `/api/remote` are exempt. SameSite cookie. Test: every non-GET route has either a CSRF or a bearer requirement. | M2, M3 |
| GHSA-j7f5-gfqm-pcx3 (Medium, 1.12.3) | The e-mail change endpoint allowed user enumeration. | `PUT /account/email` is throttled; login and forgot-password give the same response for known and unknown users. Test: response and timing differences. | M3, M4 |
| GHSA-7v3x-h7r2-34jv (Medium), CHANGELOG (v1.7.0) | A session derived from an API key stayed alive after the key was deleted. | A bearer-key request does not create a persistent session; deleting or expiring a key takes effect immediately (no cache). Test: after deletion the next request is 401. | M3, M4 |
| GHSA-g8gw-6j32-8w7g (Critical, fixed in 1.2.2; the advisory text was only partly visible), CHANGELOG (authentication bypass: running schedules, rotating DB passwords, viewing/deleting backups), GHSA-6888-7f3w-92jx (a non-admin user could list all servers) | Access to a server or resource the user has no right to. | Every resource below `{server}` (schedule, task, backup, database, allocation, subuser) is loaded scoped to the server (`WHERE id = ? AND server_id = ?`); `GET /api/client` lists only the user's own servers by default. Test: a user entitled to S1 using S2 resource identifiers gets 404/403 on every route. | M5–M15 |
| GHSA-pjmh-7xfm-r4x9 (Medium) | Rapid API key creation. | Limit (25) with locking, see 10.1 d. | M4 |
| GHSA-8c39-xppg-479c (CVE-2025-68954, High), GHSA-hr7j-63v7-vj7g (High; the advisory names no fixed version, the CHANGELOG lists it among the v1.12.1 fixes) and CHANGELOG | SFTP (and websocket) access did not end when server access was reduced, a user was deleted or a password was changed. | The reference revokes it by calling Wings `POST /api/deauthorize-user` (`{user, servers}`): subuser update/deletion, server owner change, user deletion, password change. Raptor: the same events, with a **durable outbox** and retry (a node being unreachable must not lose the revocation). Test: the outbox retries when Wings is down. | M4, M8, M9, M10 |
| GHSA-grp4-3hv5-m6vf (Wings, Medium, fixed in 1.13.2) | A revoked user could use signed download/upload URLs until they expired. | Short JWT expiry (the values are taken from the reference at the start of M6), a unique `jti`; the Wings version is pinned. | M6, M15 |
| GHSA-77pg-7gfq-9hjr (Wings, **Critical**, fixed in 1.13.1), GHSA-7w9q-4q32-wfj8 (SSRF on restore URLs), GHSA-qq22-jj8x-4wwv (SSRF on file pull) | Path traversal on the host through backup identifiers; SSRF. | The backup identifier is generated by the panel (UUID), and the `{backup}` parameter of the remote and client routes is format-checked; the `files/pull` URL is checked for scheme (`http`/`https`), length and the absence of credentials. The actual SSRF filtering is Wings' job, which is why the version is pinned. | M13, M15 |
| GHSA-pfvc-3p5h-x7h6 (Wings, **Critical**, fixed in 1.12.3) | Node config secrets could leak through egg config templates. | Wings ≥ 1.13.4. Proposal (to be refined at the start of M1b from the Wings source): `egg import` should warn about `config.*` placeholders. | M1b |
| GHSA-384w-wffr-x63q, GHSA-mgr9-6c2j-jxrq, GHSA-5822-pw57-vv37, GHSA-3q45-4vhr-c7g7, v1.12.1 (activity log) | XSS in the admin area, the file manager and the log view. | The admin Blade is not ported. The Go shell **does not put raw values into the template**: the JSON of `window.PterodactylUser` and `SiteConfiguration` is inserted with context-aware escaping (`html/template`) (`</script>`, U+2028/9). Security headers (CSP too; the reference introduced default headers in v1.12.1). Test: `</script><script>alert(1)` in the username. | M2, M3 |
| CHANGELOG (security and related entries, v1.1.3–v1.10.4) | Foreign IP addresses in the activity log; bypass of the 2FA requirement; subuser permission sets with unregistered permissions; the old owner's JWT after an owner change. | The activity API shows the IP only of the user's own entries; with the 2FA-required setting the client API returns an error; subuser permissions are validated against an allow-list; on an owner change access is revoked (outbox). | M3, M5, M9, M10 |

### 10.3 Not applicable, or only about Wings
- v1.15.2 "php2shell" (CVE-2026-109618, CVE-2026-109619), the PHP/php-fpm configuration, and the Laravel dependencies
  (e.g. CVE-2024-52301): Raptor is Go and is not affected. The Go-side counterpart is `govulncheck` in CI (M0).
- The Wings-only advisories (SFTP quota, symlink, registry credentials, chmod, config file size and so on): the Wings
  version is pinned and there is nothing to do on the panel side. To be checked separately: GHSA-8j54-xcwx-597p
  (CVE-2026-61617, SFTP write quota), because the advisory names no fixed version; before M7 it must be checked whether
  v1.13.4 contains the fix.

## 11. Pinned versions and tools

The versions go into `versions.env` (M0); the tests and CI read it. The data was read from official sources on
2026-10-22. **Rule (approved): before every install, the version and hash are re-verified on the official site, the hash
is shown to the project owner, the downloaded file's hash is computed and compared; on a mismatch nothing is installed.**
The download goes into a separate, empty folder and is used only after the hash matches.

### 11.1 Pinned versions

| Component | Version | Notes |
|---|---|---|
| Reference (Pterodactyl panel) | v1.15.2, commit `90b2aa8` | Already so, nothing to do. |
| **Wings** | **v1.13.4** (released 2026-10-14, not a pre-release) | The latest stable. The e2e tests of M7, M13 and M17 run **only with this**; at startup the test checks the running Wings version. Changing the version needs separate approval and a new e2e run. |
| Go | 1.27.3 | The `go` and `toolchain` lines of `go.mod` pin it as well. |
| Node.js | **22.23.4 LTS** (npm 10.9.9) | Changed from the 24.22.1 first planned: the reference's CI builds with Node 22, which is the configuration the old webpack/babel chain is known to work with (M3b). |
| MariaDB | 11.8 LTS (supported until 2030-02-13) | Dev and main test. The integration matrix also has 10.11 LTS. The exact point release goes into `versions.env` at install time. |
| Yarn | 1.x (classic) | `yarn.lock` is v1 format; the reference CI also uses `yarn install --frozen-lockfile`. |

### 11.2 Install list and status

Before an install, the exact file name, source, size and hash are shown. Node.js, Yarn and MariaDB are installed when they
are first needed (M3b, and the integration tests respectively); the MariaDB root password is set by the project owner.

**Official sources on the original list:**

| Tool | Source | File and verification |
|---|---|---|
| Go 1.27.3 | go.dev/dl | `go1.27.3.windows-amd64.msi`, 66,146,304 bytes, SHA-256 `89ec8325f667c674fe1b94a5a88e26932aa12323589881927ff54da99d4b7faa` (per the go.dev JSON) – **installed** |
| Node.js 22.23.4 LTS | nodejs.org/dist/v22.23.4/ | `node-v22.23.4-x64.msi`, 31,682,560 bytes, SHA-256 `ca9303d3a12e8bec6de829fcb7812fc776930e0ce5c030328a24984ef928427e` (from `SHASUMS256.txt`) – **installed** |
| MariaDB 11.8 LTS | mariadb.org/download | Windows x64 `.msi`, the hash from the page; the root password is **set by the project owner**, it is not stored by the assistant, and the test DSN lives in an environment variable |

**Not on the original list; per decision N4: sqlc, govulncheck and Yarn approved, WSL2/Docker/Wings only before M7:**

| Tool | Source | Why it is needed |
|---|---|---|
| sqlc | `go install github.com/sqlc-dev/sqlc/cmd/sqlc@<pinned version>` (sqlc's official repository, via the Go module proxy) – **installed (v1.31.1)** | per CLAUDE.md database access is generated by sqlc |
| govulncheck | `go install golang.org/x/vuln/cmd/govulncheck@<pinned version>` (the Go team's official module) – **installed (v1.9.0)** | Go-side vulnerability check in CI |
| Yarn classic | `npm install -g yarn@1.22.22` (npm registry) – **installed** | frontend build and audit; because of `yarn.lock` |
| WSL2 + Ubuntu LTS, Docker Engine, Wings v1.13.4 binary | Microsoft (`wsl --install`), Canonical, docs.docker.com, github.com/pterodactyl/wings/releases (the hash from the release page) | for the real Wings e2e test; only before M7 starts, as a separate step |

Go dependencies (e.g. Echo) are pinned by `go.mod`/`go.sum`; every new dependency is shown when it is added.

## 12. Frontend (`web/`) build and audit

The frontend is a copy of `reference/resources/scripts` + the build config from v1.15.2; because of the old dependencies
(React 16, webpack 5, ~1300 `yarn.lock` entries) the audit is part of M3b.

- **Build**: `yarn install --frozen-lockfile`, `yarn tsc`, `yarn lint`, `yarn build:production` (as in the reference CI).
  Node version: per 11.1, with a compatibility trial.
- **Audit**: the lockfile is `yarn.lock`, so **`yarn audit`** runs on the committed lockfile, because that examines exactly
  the shipped versions. `scripts/frontend-audit.js` runs it and matches the findings against the package versions that
  webpack puts in the bundle; the policy below is applied by that script (implemented in M3b). The requested **`npm audit` cannot be used directly** (there is no `package-lock.json`; if
  `npm install --package-lock-only` re-resolved the versions, that would no longer be the shipped package). As a
  cross-check `npm audit` can be run on a generated lockfile, but `yarn audit` is authoritative.
- **Triage**: high and critical findings in runtime dependencies (those that reach the browser) block, unless there is a
  documented exception (e.g. an unreachable code path). Findings in build tools (dev) are tracked but do not block.
- **Fixes**: a `resolutions` override or a dependency update; every change goes into `docs/frontend-audit.md` (finding,
  triage, decision, test). Changes stay minimal, and the React source is touched only where the fix requires it.
- **CI**: the audit runs on every run; a new blocking finding stops the build.
- **Licence**: the frontend is MIT licensed; its copyright text goes into `NOTICE`.

## 13. Cutover and secret rotation (draft for M17)

Starting point (N2): **a leak of the `APP_KEY` is assumed.** It follows that the old machine and every file on it are
treated as untrustworthy, and every secret protected by the old encryption counts as compromised. As long as the old
columns exist, everything can be decrypted by anyone who knows the key and has the database.

### 13.1 Install model: a clean machine, only the database comes over from the old machine

- Raptor goes onto a **clean machine or a clean installation** (a new server/VM, or a reinstalled operating system). Not
  onto the old panel's machine, not into the old directories.
- From the old machine **only the content of the database** is carried over, in a `mysqldump`. Nothing else: no `.env`,
  no `storage/`, no `vendor/`, no PHP files, cron entries, web server or php-fpm configuration, certificates, SSH keys or
  scripts.
- The single exception is the value of `APP_KEY`, and only temporarily: for the duration of `raptor migrate-secrets`, in
  the `RAPTOR_LEGACY_APP_KEY` environment variable (never in a file, never in a log, not in the shell history either),
  destroyed afterwards.
- The database goes into a **new MariaDB instance** on the new machine, with a **new database user and a new password**
  (the old DB credentials are not reused), by dump import; the data directory is not copied.
- New elements generated on the new machine: `RAPTOR_SECRET_KEY` (`raptor keygen`), the TLS certificate, a service
  account with minimal privileges.
- The Wings nodes stay where they are (separate machines), but their configuration is updated with the new node tokens
  and the new panel URL (13.3).
- The imported database is **untrusted data**: the old machine may have been compromised, so after the import the check
  in 13.5 follows, before anything is put into production use.

### 13.2 Steps
1. **Preparation**: build the new machine with the pinned versions (chapter 11); a `mysqldump` of the old machine's DB in
   a maintenance window; back up the old node `config.yml` files (only for the way back, not onto the new machine); a
   full rehearsal on a test machine, a DB copy and a test node; `raptor db check`.
2. **Cutover**: stop the old panel, **final dump**, import into the new DB (with the new user), the check in 13.5,
   `raptor migrate-secrets` (old columns untouched), the **rotation** (13.3), start Raptor, update and restart the nodes'
   Wings configuration, smoke test. Every user logs in again.
3. **Validation**: a short, pre-fixed period. During it the old machine and its DB stay untouched and switched off (13.4).
4. **Closing**: `raptor migrate-secrets --finalize` with confirmation (neutralising the old columns in the new DB, with a
   fresh backup first); permanently shut down the old machine; destroy the old `APP_KEY`, `.env` and the old DB credentials.

### 13.3 What is rotated (part of the cutover)

| Secret | Command (M1b/M14/M17) | Effect and warning |
|---|---|---|
| **Node tokens** (`nodes.daemon_token`, `daemon_token_id`) | `raptor node rotate-token <id>`, output: the new Wings `config.yml` part | Raptor starts with the new tokens already, so the old tokens are never valid on the new panel. Every node's `config.yml` must be updated and Wings restarted; until then the panel cannot reach that node. **Game servers keep running**, they just cannot be managed from the panel. JWTs issued by the panel become invalid as well. Node by node, in a maintenance window. |
| **API keys** (account `ptlc_`, application `ptla_`) | `raptor apikey revoke --all` | The raw value of the keys was recoverable, so they count as compromised, and cannot be issued again (they are visible only at creation). New keys must be created; users and integrations should be notified in advance. |
| **Database host passwords** (`database_hosts.password`) | `raptor dbhost rotate-password <id>` | The old password (from the migrated copy) is used only to connect to the host and set the new one; the record and the account on the host change together. |
| **Server database passwords** (`databases.password`) | `raptor dbhost rotate-all` (explicitly requested) | **Passwords written into game server configurations break**, so only together with a notification and in agreement with the owners. Because of the `APP_KEY` leak, postponing is a risk; this is decided separately before the cutover. |
| **2FA secrets** (`users.totp_secret`) | `raptor user reset-2fa --all` | Users must register TOTP again. So that 2FA does not become empty, Raptor keeps a "re-enrolment required" flag in the side table: after the password, the user proceeds only after re-enrolling. New recovery tokens are generated as well. |
| Mail (SMTP) password, reCAPTCHA keys, S3 and other credentials in the old `.env` and the `settings` table | manual, at the provider | Rotate everything that was on the old machine. |
| `APP_KEY` | – | Raptor **does not reuse it**; a new `RAPTOR_SECRET_KEY` is made, and its backup is kept separately in a safe place. |

`HASHIDS_SALT` is not a secret, and changing it would change the identifiers visible in URLs, so it stays.

With timing A, `migrate-secrets` is mainly needed to read `database_hosts.password` (to reach the host during rotation);
node tokens, API keys and 2FA secrets are replaced by new values.

### 13.4 Way back
- The old machine and its DB stay **switched off and untouched** until the end of validation. There is no routine
  rollback, because the old machine counts as compromised; in an emergency the old panel may be started only on an
  isolated network.
- What arose after the cutover (new keys, node tokens, 2FA settings, the forced logout) is invisible to the old panel
  (2.3); password hashes stay compatible. Because the node tokens were rotated, the Wings configurations have to be
  restored (that is why backing up the old `config.yml` files in step 1 of 13.2 is mandatory). All of this justifies a
  full rehearsal before the cutover.

### 13.5 Checking the imported database (proposal, M17)
A new, read-only `raptor audit` report on the imported DB, reviewed by a human; it deletes nothing by itself:
- `root_admin` users and recently created or modified accounts,
- application and account API keys, user SSH keys, the permissions of subusers,
- the `startup` command, install script and docker image of eggs compared with the reference `database/Seeders/eggs`
  files (the install script runs on the node),
- schedules and tasks (`command`, `power`, `backup`), source paths of mounts, node records (`fqdn`, scheme), database
  hosts, the `settings` table (mail server, 2FA requirement),
- suspicious `activity_logs` events.
The new command is built in after separate approval, at the start of M17.

## 14. Next step

M0, M1 and M1b were approved and started. The sqlc part of M1 and the integration tests of the Raptor-owned tables wait for
the `docs/schema/` files and a MariaDB instance; the status is recorded in the `docs/modules/00-…`, `01-…` and `01b-…`
notes.
