# Operating the panel: health, API limits, mail settings, the admin commands

Status: **done over interfaces** (2026-11-02): `internal/api/healthapi`, `Group.AfterAuth` and `authz.RateKey`, `mail.OptionsFromSettings`,
`internal/admincli`. Registering the commands and the routes in the program waits for the stores.

## Health: `/healthz` and `/readyz`
Public, no login, `Cache-Control: no-store`, optionally limited per client address. `/healthz` answers `{"status":"ok"}` while the
process answers. `/readyz` runs the named checks (the database ping, the outbox, and so on) all at once under one time limit (3 s by
default); the answer is 200 `ok` or 503 `unavailable` with `ok` or `failed` for each check. A check that panics or does not finish has
failed. What failed is in the log, never in the answer: an anonymous caller learns that something is wrong, not what.

## Limits on the two APIs
The reference limits both APIs to 240 requests a minute for each user, by the user's UUID and by the address when there is no user.
Raptor gives the router `Group.AfterAuth(mw...)`: middleware that runs after the route has been authorized, so it can tell who asks, and
`authz.RateKey` as the key. The wiring is `group.AfterAuth(middleware.Throttle(limiter, authz.RateKey))` with a `Limiter` of 240 per
minute on `/api/client` and another on `/api/application`. A request the authorizer refuses is not counted (the sign-in throttles cover
guessing), so no stranger can use a user's allowance up.

## Mail from the settings
`mail.OptionsFromSettings(get, password, hello)` makes the options of a `Sender` from the settings table (`mail:mailers:smtp:host`, `port`,
`encryption`, `username`, `mail:from:address`) and the password, which is not a setting. The reference's `encryption` is read as it reads
it: `tls` is STARTTLS, `ssl` is TLS from the first byte, and nothing is no protection, which `NewSender` allows only for this machine.
No host is `ErrNotConfigured`: the panel then sends no mail, and says so. The display name of the sender (`mail:from:name`) is not used yet.

## The admin commands (`internal/admincli`)
`user create|delete|disable-2fa`, `apikey create`, `location create`, `node create`, `nest create`, `egg import`, `settings set|list`.
Each reads its flags with `flag` (a mistake is a usage error, exit code 2), calls `internal/admin`, and writes **what is secret to the
standard output, once, and nothing else there**: the generated password of a user, the whole key, the `config.yml` of a node. What the
command says about itself goes to the standard error. A password is never an argument: `--password-stdin` reads the first line of the
standard input, and an empty line is refused (no one is given a password they did not ask for). The commands are not registered in
`cmd/raptor` yet: they need a store.

## Tests
`healthapi` 100 %, `admincli` 97 %, `mail` 94 %. The mutation spot checks killed every mutant (streams of the secrets, required flags, the
empty password line, the mapping of the encryption, the status codes of the checks, the order of the router's middleware).

## Since: the pieces around the routes
The loops and the web server are in `19a-app-and-inventory.md` (`internal/app`), the pages in `03i-pages.md`, and the full list of what the SQL layer has to
implement in `../store-contract.md`. The plan to the first test is `../first-test.md`.
