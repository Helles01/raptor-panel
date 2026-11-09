# Advisories that `scripts/check.sh` accepts

`scripts/check.sh` runs `govulncheck`, which exits with 3 when it finds a known vulnerability in code that is called. The identifiers below are accepted by name, and only these: any other finding fails the
check, and so does a failure of the tool or an output the script cannot read. Accepted 2026-11-09 by the owner, after the review below. **Both advisories are for Echo v4 (`github.com/labstack/echo/v4`, we use
v4.16.0) and neither has a fixed version** ("Fixed in: N/A"), so waiting for an update does not help; the choice was to leave the check red or to accept what is not exposed.

`govulncheck` reports them because the advisories name the module as a whole, and the program calls Echo (`echo.New`, `Router`, `Context.JSON`, …). Its "example traces" are therefore any call into Echo and say nothing about the
dangerous part. The dangerous parts are these.

## GO-2026-6872 — rate limiter bypass through a spoofed `X-Forwarded-For`, in the default configuration
Echo's `Context.RealIP()` believes `X-Forwarded-For` and `X-Real-IP` from anybody unless the application sets `Echo.IPExtractor`. Raptor sets it, once, for the only Echo it makes:
`internal/api/server.go` sets `e.IPExtractor = trust.ClientIP` right after `echo.New()`, whether or not a list of trusted proxies was given. `trust.ClientIP` believes the headers only from the
addresses in `RAPTOR_TRUSTED_PROXIES` (default: none, then the address the connection came from). `Context.RealIP()` is called in one place (`remoteapi`, the report of a restore) and goes through the extractor. The limits
(login, API, node API) are Raptor's own (`internal/api/middleware`), keyed on the same address, not Echo's rate limiter. `docs/cutover.md` and `raptor doctor` say that a panel behind a proxy needs `RAPTOR_TRUSTED_PROXIES`.

## GO-2026-6875 — a reverse proxy forwards an attacker-supplied `X-Real-IP` to the upstream by default
The affected symbols are Echo's proxy middleware (`middleware.Proxy`) and, in the advisory's wording for the v5 line, `Context.Scheme`, `NewWithConfig` and the `Static`, `Secure` and `MethodOverride` middleware. Raptor
imports **no package of `github.com/labstack/echo/v4/middleware`** (it has its own middleware in `internal/api/middleware`), never calls `Scheme()`, never calls `NewWithConfig`, and is not a reverse proxy: it forwards no request to
anything. The panel talks to the nodes with its own client (`internal/wings`) and builds those requests itself.

## What keeps this true
`internal/api/advisories_test.go` fails, with a message that names the advisory, if any non-test file
- imports `github.com/labstack/echo/v4/middleware`,
- calls `NewWithConfig` or `Scheme()`,
- makes an Echo anywhere but in `internal/api/server.go`,

or if a server made by `api.New` is left without an extractor of the client address. Each of the four was made to fail on purpose before this was written down.

## When to look again
When Echo publishes a fixed version (update, and delete the identifier from `accepted_vulns` in `scripts/check.sh` once `govulncheck` stops reporting it), when Raptor moves to Echo v5, or when one of the tests above
fails (do not change the test: either the code goes back or the acceptance is wrong). An identifier that `govulncheck` no longer reports can stay in the list without harm, but should be removed when noticed.
