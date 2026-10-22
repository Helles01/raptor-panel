# M2 – HTTP foundation

Status: **done** (2026-10-22). `scripts/check.sh` is green. The module has no database dependency, so it was built while
M1/M1b wait for the schema. There are no endpoints yet and no `raptor serve` command: both arrive with M3, which needs
a session store and an `Authorizer`.

## What was built
| Package | Content |
|---|---|
| `internal/api/apierr` | The API error type and its JSON form, `{"errors":[{"code","status","detail"}]}`. The codes keep the reference's exception names (`NotFoundHttpException`, `AccessDeniedHttpException`, `ValidationException`, `ThrottleRequestsException`, `TwoFactorAuthRequiredException`, …), because the React frontend matches on at least one of them. `status` is a string, as in the reference. Validation errors have one entry per field with `meta.source_field` and `meta.rule`. `Retry-After` is sent for 429. An internal error carries its cause for the logs only and answers with the reference's generic message. |
| `internal/api/validate` | Allow-list helpers (`Required`, `Length`, `Matches`, `OneOf`, `IntRange`, `UUID`, `Email`) with Laravel's rule names and messages, first failure per field, and `DecodeJSON` (size limit, empty body, trailing data, wrong types). |
| `internal/api/resp` | The Fractal-style envelopes: `Item` (`object`, `attributes`, `relationships`) and `List` (`object: "list"`, `data`, `meta` with free keys such as `startup_command`), and the `meta.pagination` block (`total`, `count`, `per_page`, `current_page`, `total_pages`, `links`, which is `[]` when empty). `PageParams` reads `page` and `per_page`. |
| `internal/api/middleware` | `ProxyTrust` (client address: `X-Forwarded-For` is believed only from configured proxies, walked from the nearest hop), `Limiter` + `Throttle` (fixed window, bounded memory, empty key fails closed with a 500), `SecurityHeaders` (the reference's four defaults), `RequestID` (always fresh, never taken from the client), `RequestLogger` (method, path, status, size, duration, address, request ID; never the query, headers or bodies), `Recover`, and `NewRedactingHandler` for slog. |
| `internal/api` | `Router` with a **mandatory permission descriptor** per route (`Guest`, `Public`, `Authenticated`, `Admin`, `ApplicationKey(resource, access)`, `ServerPermission(name)`, `Daemon`), an `Authorizer` interface that runs after the route's own middleware (so a throttle also counts unauthenticated attempts), duplicate and invalid registrations panic, `Verify` catches any route added without a descriptor, and `Server`, which wires the fixed chain (recover, request ID, request log, security headers), the central error handler and the client address extractor. |
| `internal/config` | New `http` section: `listen` and `trusted_proxies` (`RAPTOR_HTTP_LISTEN`, `RAPTOR_TRUSTED_PROXIES`). A range that covers every address (`0.0.0.0/0`, `::/0`) is refused. |

## Known bugs covered (plan chapter 10)
- **GHSA-xvc3-826v-xf47 (shared rate-limit bucket):** a throttle key must separate clients; `KeyByIP` uses the real client
  address, `KeyByIPAnd` adds a second value (the login name), and an empty key is a 500 and is never counted. Tests: one
  address cannot exhaust another's bucket; rotating `X-Forwarded-For` from an untrusted client does not escape the limit;
  a mutation check showed that a constant key fails these tests.
- **CVE-2024-49762 (password in a log):** the redacting handler masks sensitive attribute names and secret-looking text
  (API keys, bearer credentials, JWTs, Raptor sealed values, `token=` style parameters) in messages, strings, errors,
  groups and `With` attributes. The request log never includes the query string, headers or bodies.
- **Permission on every route:** registration without a valid descriptor panics, and `Verify` fails if Echo knows a route
  the Router does not.
- **Information leaks in errors:** internal causes and panic values reach the log (redacted), never the client.

## What matches the reference
The error and list/item envelopes, the pagination block, the four default security headers, and the generic 500 message.

## Deliberate differences (`docs/compat.md`: C-010, C-011)
- Pagination links are relative (`/path?page=2`), not absolute URLs, and `per_page` is capped at 100 with a 422 for bad
  values (C-010).
- Only the error codes the reference's frontend depends on are guaranteed; other codes use the reference's names where an
  equivalent exists (C-011).

## Dependency
`github.com/labstack/echo/v4` **v4.16.0**, module hash `h1:cFqqpqVNmSVyn4nvsXHp5rU4aVLYG3hx4fGWc3FngBk=` (equals
sum.golang.org). v4.16.1 was published on the same day this module was written (a few hours earlier) and fixes issues in
features Raptor does not use (WebSocket proxy, body dump, decompression); it is not taken yet, to give a fresh release
some time. `govulncheck` finds nothing in called code with v4.16.0. Revisit when v4.16.1 is a few weeks old.

## Left for later modules
- `raptor serve`, the `http.Server` timeouts and graceful shutdown, a real `Authorizer` (M3, M5, M7).
- CSRF protection and a Content-Security-Policy (M3, they need the session and the page shell).
- Rate-limit settings per API (the reference's `APP_API_CLIENT_RATELIMIT`/`APP_API_APPLICATION_RATELIMIT`, 256/minute) are
  wired when the client and application groups exist.
