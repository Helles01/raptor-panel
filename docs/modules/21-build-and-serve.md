# 21 — `app.Build` and `raptor serve`: the panel, wired

Written 2026-11-03. `app.Build(ctx, Input)` makes the panel from the configuration and the stores: the router with every endpoint package behind its authorizer, and the loops that run beside it.
`raptor serve` (`cmd/raptor/cmd_serve.go`) reads and checks the configuration, opens the database, **refuses to start on a schema that is not the one Raptor is written against** (the reference's
migrations missing or unknown, or Raptor's own migrations pending: it says to run `raptor db check` / `raptor db migrate` and changes nothing), loads and checks the frontend build, builds the panel and runs
it until Ctrl-C or SIGTERM. `storesOf` (`cmd/raptor/stores.go`) names the SQL implementation of every field of `app.Stores`; `Stores.PanelMissing()` is what `Build` needs of it.

## The router
- **Every route sits behind the session and the anti-CSRF check**, in this order: session loaded (`middleware.LoadSession`), CSRF (`middleware.CSRF`: a state-changing request needs the token of its session and, if it sends an `Origin`,
  the panel's own; a request with a bearer token is exempt, as a browser cannot be made to send one), the authorizer of the route, and for the two APIs the limit. The key of the CSRF tokens is made from the secret key
  of the panel (`HMAC-SHA256(secret, "raptor/csrf-key/v1")`), so the two are not the same bytes.
- **The two APIs are limited per user, after the authorizer**: 240 a minute for `/api/client/*` and for `/api/application/*`, counted by the UUID of the user (`authz.RateKey`; the address when no user is known). A guest that is refused
  by the authorizer counts against nobody. The sign-in routes are limited per address, the pages and `/healthz` as in `app.Limits`.
- The cookie is `Secure` (and `__Host-`-prefixed) when `app.url` is `https://`; with `http://` it is not, which is for a local test only.
- Sessions end after 12 hours without use or a week after they were made (`SessionIdle`, `SessionLifetime`).

## What has no place in the configuration yet
`app.DatabasesEnabled`, `DatabasesAllowRandom`, `HideAdminActivity`, `BackupThrottleLimit`/`Period` (2 in 10 minutes), and the notices of finished installations (both on) are constants with the
reference's values. The owners' own allocations (on/off and the port range, the settings `pterodactyl:client_features:allocations:*`) are read **at each request** (`allocapi.Policy`, three small reads), so
`raptor settings set …` applies to a panel that is running; they are read once at start as well, so that a value stored wrongly stops the start rather than failing a request later. The name of the panel
used in mail and as the issuer of two-factor codes is read **once at start**: a changed value needs a restart (it is shown by the page at once; only the mail and the issuer wait). The activity texts of the frontend (`en/activity.json`) tell which properties of an entry its text shows.

## The adapters (`internal/app/adapters.go`)
- `reinstaller` (`settingsapi.Reinstaller`) finds the node of the server and tells it to run the install script again; `oldNodes` (`remoteapi.OldNodes`) deletes a moved server on the node it was on, as the node is stored now.
- `settingsSender` (`outbox.Sender`) reads the mail settings **at the moment of sending**, so a change needs no restart. No mail server set up → the job is set aside (permanent); settings that are wrong, or a database that is
  away → tried again (the outbox backs off, ten tries). The password of the mail server is read from `raptor_secrets` (`store.MailSettings`); on a fresh installation `raptor settings set-mail-password --password-stdin` sets it (`docs/modules/24-mail-password.md`); from an old panel it arrives with
  `raptor migrate-secrets`.

## The loops
The outbox (every `jobs.outbox_seconds`; mail and the orders to take a user's access from a node), the scheduler (every `jobs.schedule_seconds`, after clearing the marks a crash left), and the housekeeping
(reset tokens and sessions hourly, orphaned backups every 30 minutes, the activity log daily; the last two switch off with `jobs.backup_prune_minutes` / `jobs.activity_keep_days` = 0). A loop that stops by itself stops the panel.

## What the first end-to-end test found
- **`relationships` belong inside `attributes`.** The reference's serializer merges what an item includes into the item's own data (`PterodactylSerializer::mergeIncludes`), so an included resource is
  `{"object":…,"attributes":{…,"relationships":{…}}}`, and the frontend reads `attributes.relationships`. `resp.Item` wrote them beside the attributes, and ten packages' tests had been written to agree with it. It is fixed
  in `resp.Item.MarshalJSON` (`TestRelationshipsAreInsideTheAttributes` pins the exact bytes) and in the tests; without the end-to-end test the schedules page, the server's allocations and variables, and every
  `?include=` would have shown nothing in the browser.
- The built panel serves **exactly** the 137 routes of the reference plus `/healthz`, `/readyz` and `/assets/*`.
- A wrong password is a 400 (`DisplayException`), a node that cannot be reached a 504, as in the reference.

## Tests
`cmd/raptor/cmd_serve_integration_test.go` (the whole panel, with two-factor and the limits), `cmd_routes_integration_test.go`, `internal/app/adapters_test.go`, `internal/db`: `PendingMigrations`.
Mutants killed: the CSRF check, the session loading, the limit of each API.

## The first run in a browser (2026-11-03)
A development panel on `127.0.0.1:8000` over its own database (`raptor_dev`, credentials in the git-ignored `.raptor-dev.env`), opened in the built-in browser pane. What was seen:
- The login, password-request and sign-in-redirect pages render, the fonts and bundles load, and **the strict CSP (`default-src 'none'`, script nonce, `img-src 'self' data:`) blocked nothing**: the console had no violation.
- One fault: the logo of the login box was a broken image, because the frontend asks for `/assets/svgs/pterodactyl.svg`, a file of the reference's `public/` folder that is not part of the build. The component now imports
  the logo through the build (`03b-frontend-build.md`); after a rebuild the page has no 404.
- The signed-in pages need a sign-in by a person: the password of a development account is not read out of the file by the assistant.
