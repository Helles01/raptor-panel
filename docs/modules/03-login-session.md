# M3 – Login and session

Status: **groundwork done** (2026-10-22): everything that does not need the database. The endpoints themselves
(`POST /auth/login`, `/auth/login/checkpoint`, `/auth/logout`, password reminder and reset, API key authentication, the
`Authorizer`, `raptor serve`) wait for the schema, because they read users, recovery tokens, API keys and the sessions
table. All the pieces below are tested on their own.

## Done
| Package | Content |
|---|---|
| `internal/auth/totp` | RFC 6238 TOTP (HMAC-SHA1, 6 digits, 30 s; checked against the RFC's test vectors), secret generation (32 base32 characters, bias-free), the `otpauth://` URI, and `Verify` with **replay protection**: only a step newer than the last accepted one is accepted (CVE-2025-69197). Every candidate step is compared in constant time without stopping early. |
| `internal/auth/session` | Sessions with a random 256-bit cookie value. The store only ever sees the SHA-256 of it. Idle timeout and maximum lifetime. **Login always issues a new identifier** and deletes the old one (session fixation). Cookie: `__Host-raptor_session` (Secure, HttpOnly, SameSite=Lax, Path=/, no Domain; plain `raptor_session` for local development). The 2FA "confirmation" (64 random characters, 5 minutes, constant-time compare, tied to the session) for the login checkpoint. Store interface that matches the `sessions` table columns, plus an in-memory store. |
| `internal/auth/csrf` | Anti-CSRF tokens `random.MAC` bound to the session identifier (not a plain double-submit cookie). Only the canonical spelling of a token is accepted (Go's base64 decoder would skip line breaks). |
| `internal/api/middleware` | `LoadSession` (loads the session, clears an invalid cookie), `CSRF` (unsafe methods need the `X-XSRF-TOKEN` header that matches the session; optional allowed-origin list; `Authorization: Bearer` requests are exempt), `SetCSRFCookie` (`XSRF-TOKEN`, readable by the frontend's HTTP client). |
| `internal/api/web` | **Locale endpoint** (`GET /locales/locale.json`): the locales and namespaces are a fixed in-memory table built from files embedded in the binary; the request values are only looked up, they never form a path (CVE-2025-49132, plan 10.1 c). Strict patterns, a repeated or array-style parameter is refused, unknown values give 404. **Page shell**: `html/template` page that sets `window.PterodactylUser` and `window.SiteConfiguration` through JavaScript-context escaping, loads `main.js` with its SHA-384 integrity value, and sends a **Content-Security-Policy** with a per-page nonce, `Cache-Control: no-store`. The manifest of the frontend build is validated before anything from it is put into a page. **Assets handler** for `/assets/`: plain files only, no listing, no nested paths, no dot files, a year of caching for hashed files. |
| `scripts/convert-lang.js` | Converts the reference's PHP translation file `activity.php` to `internal/api/web/locales/en/activity.json` (with the `:name` → `{{name}}` rewrite). The frontend only uses the `activity` namespace (plus the empty default `translation`). |

## Known bugs covered (plan chapter 10)
- CVE-2025-49132 (locale endpoint): see above; the tests include `../`, encoded and double-encoded traversal, backslash,
  NUL bytes, upper case, over-long values, empty, repeated and array-style parameters.
- CVE-2025-69197 (TOTP replay): `Verify` with the last accepted step; a test replays the same code.
- GHSA-5vfx-8w6m-h3v4 (2FA confirmation flaw): the confirmation is random, bound to the session, short-lived and compared
  in constant time.
- XSS in the page data: a test puts `</script><script>…`, quotes, template literal syntax and U+2028/U+2029 in every
  field and checks that the page keeps exactly three script tags and that the data arrives unchanged.
- CSRF (GHSA-wwgq-9jhf-qgw6, GHSA-m49f-hcxp-6hm6): the CSRF middleware above; the login and logout routes will use it.

## What matches the reference
The `window.PterodactylUser` and `window.SiteConfiguration` shapes (checked against `App.tsx`), the page structure and
body classes of the two shells, the locale response shape and cache policy, `XSRF-TOKEN` / `X-XSRF-TOKEN`, the 5-minute
confirmation token, the TOTP secret length and `otpauth://` URI.

## Deliberate differences (`docs/compat.md`: C-012 to C-015)
- The TOTP window is ±1 step by default, not ±4 (C-012).
- Session lifetime, no remember-me cookie (C-013).
- The locale endpoint answers only what exists, 404 otherwise (C-014).
- A strict Content-Security-Policy that the reference does not send (C-015). **Not yet checked in a browser**: the console
  and file transfer talk to the Wings nodes, whose origins must be listed in `ShellOptions.ConnectSources`; this is
  verified in the first-usable milestone.

## Still to do in M3 (waits for the schema)
- Database store for sessions, user, recovery token and API key queries (sqlc), the `Authorizer`.
- `POST /auth/login`, `/auth/login/checkpoint` (password check, TOTP or recovery token, throttle keys from M2),
  `/auth/logout`, forgot and reset password (needs `internal/mail`), `ptlc_` key authentication (an `Authorization`
  header must not fall back to the cookie), the 2FA-required setting, and the `raptor serve` command with `http.Server`
  timeouts and graceful shutdown.
