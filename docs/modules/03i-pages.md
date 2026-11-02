# The pages: the shell, the build files, the translations

Status: **done over interfaces** (2026-11-02): `internal/api/pageapi`. The two stores it asks (the signed-in user's data, the panel's name and
language) wait for the schema; `internal/api/web` (the shell, the locales, the assets handler) was written earlier and is used as it is.

## Routes
All are `GET` and all are registered `Public`. A page is for the signed in or for guests, and that is decided in the handler, because a
browser that asks for a page must be sent to the sign-in page, where the authorizer would answer with a 401 in JSON.

| Route | Does |
|---|---|
| `/`, `/account` | the application shell with `window.PterodactylUser`; no session, a guest session, or a session whose account is gone → `302 /auth/login` |
| `/auth/login`, `/auth/password`, `/auth/password/reset/:token`, `/auth/*` | the sign-in shell without a user; a signed-in person → `302 /` |
| `/locales/locale.json` | `web.Locales.Handler()` (the allow-list of locales and namespaces, CVE-2025-49132), limited per address |
| `/assets/*` | the build files, `web.AssetsHandler` (plain names only, a year of caching, `manifest.json` never cached) |
| `/*` | the catch-all of the reference: the application shell, except below `api`, `auth`, `admin` and `daemon`, which are 404 in JSON |

## Rules worth knowing
- A session with a user whose row is gone is treated as a guest on **both** pages, so the two redirects can never send a person back and forth.
- The page is rendered with `Cache-Control: no-store`, a nonce-based CSP, and the user and site data as values for `html/template`, so a
  user name such as `</script><script>alert(1)</script>` stays inside the script (a handler test pins this).
- The token in a reset address is for the frontend only; the server never writes it into the page (a test checks).
- A store that fails gives the generic 500; nothing of the store's error reaches the page.
- Only the first part of a path decides what is reserved: `/apiary` is the application's.

## Different from the reference
Nothing that changes what a browser sees. The reference sends a guest to the login page and a signed-in person away from it exactly so. Not
done: the `intended` URL after login (the reference's frontend ignores it as well), reCAPTCHA (always off, `web.Site`).

## Tests
97 % statement coverage; 16 mutants killed (the redirect targets, the guest-session check — with a store that wrongly knows user 0 —, the
reserved prefixes, the status code of the redirect, `no-store` on redirects, both store failures, the locale limiter and both routes).
