# The rest of what the program needs around the routes

Status: **done** (2026-11-03). Nothing here needs the schema. What still needs the stores is `app.Build` (every handler from `app.Stores`) and the
`raptor serve` command.

## Configuration (`internal/config/runtime.go`)
New sections, with the defaults in brackets, all validated with allow-lists and unknown keys refused (the file is read with `KnownFields`):
- `log.level` (`info`; debug, info, warn, error) and `log.format` (`text`; text, json), env `RAPTOR_LOG_LEVEL`, `RAPTOR_LOG_FORMAT`;
- `jobs.outbox_seconds` (5), `jobs.schedule_seconds` (60, at least 5), `jobs.backup_prune_minutes` (360, 0 = off), `jobs.activity_keep_days` (90, 0 = off);
- `web.assets_dir` (`web/public/assets`), env `RAPTOR_ASSETS_DIR`;
- `db_hosts.tls` (`false`; false, true, skip-verify, preferred), env `RAPTOR_DB_HOSTS_TLS`.
- `backups.driver` (`wings`; wings, s3), `backups.presigned_minutes` (60), `backups.max_part_size` (5 GiB), and the bucket `backups.s3.*` (endpoint, region, bucket, access_key, secret_key, path_style, storage_class); env
  `RAPTOR_BACKUPS_DRIVER`, `RAPTOR_BACKUPS_PRESIGNED_MINUTES`, `RAPTOR_BACKUPS_MAX_PART_SIZE`, `RAPTOR_BACKUPS_S3_ENDPOINT|REGION|BUCKET|ACCESS_KEY|SECRET_KEY|PATH_STYLE|STORAGE_CLASS` (`25-s3-backups.md`).
A key that is left out of the file keeps its default; the environment wins over the file.

## `internal/logging`
`logging.New(cfg.Log, os.Stderr)`: text or JSON, the level of the configuration, times in UTC, and **always** behind the redacting handler, so a
password, a token or an API key in a message or an attribute is replaced before it is written (a test logs a bearer token, a key, a `password`
attribute and an error with `token=…` in both formats).

## `app.Limits`
All the limiters of the panel in one struct and one table (`limits.go`): the two APIs 240 a minute, login 10 and password reset 10 a minute,
the password link 2 a minute and 5 an hour for an address, and the per-server limits from the endpoint packages' own constants. A test pins every
number and its window, that none is shared with another and that each key has its own share.

## The console and the Content-Security-Policy (`web`, `pageapi`)
The page's policy allowed only fixed origins to connect to, but the browser talks to the **nodes** directly (the console's web socket, file
transfers), and nodes are added while the panel runs; the console would have been blocked by the browser on every node added after the start.
- `web.NodeSources(scheme, fqdn, port)` gives the two origins of a node (`https://…` and `wss://…`).
- `Shell.RenderWith` / `ContentSecurityPolicyWith(nonce, extra)` add them to `connect-src` only: an origin that is not of the plain form is left out
  (a row of the table cannot add a directive), duplicates and `'self'` are not repeated, and at most `MaxExtraSources` (500) are added.
- `pageapi.Deps.Origins` (`ConnectSources(ctx)`, optional) is asked for the list at most every 30 s; if the store fails the last list is kept and the failure is
  logged, so a hiccup does not take the console from everybody. Only the page of the signed in gets them; the sign-in pages get none.

## `app.LoadFrontend`
Reads `manifest.json`, checks every file it names (present, a plain file) and the SHA-384 of every script against the manifest, then builds the shell
and the locales. A missing or changed file stops the start with its name, instead of a page that the browser blocks without saying why.

## `app.Run`, `app.Signals`, `app.Stores`
- `Run(ctx, Options)` opens the address first (a taken port stops the panel before any loop runs), then runs the web server and the loops under the
  supervisor; `Signals(ctx)` ends the context on Ctrl-C or SIGTERM.
- `Stores` is the 66 interfaces of `docs/store-contract.md` as fields; `Missing()` names those that are not set.

## `scripts/dump-schema.sh`
Makes the three schema files from the old panel's database (see `docs/schema/README.md`): read-only, the password typed on the terminal and handed to the
client programs in a file only the user can read, refuses a dump with a single row or one that does not look like the panel's database. Tested by
`scripts/dump-schema-test.sh` with stand-ins for `mysqldump` and `mysql` (14 cases, run by `scripts/check.sh`).

## Tests
`config` 98 %, `logging` 94 %, `app` 97 %, `pageapi` 98 %, `web` 94 %. Mutants killed: every boundary of the configuration, the level and the
format of the log, the cache of the origins (the time limit and its boundary, a failure kept as a fetch, a failure that must not clear the list),
which pages get the origins, the cap and the pattern of the policy. Survivors that are equivalent or not testable here: `'self'` given as an extra origin
(already in the list), the default time limit on Windows (the clock is too coarse to tell 1 ns from 30 s), the SIGTERM registration.
