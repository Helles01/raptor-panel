# M3 – the sign-in endpoints

Status: **endpoints done over storage interfaces** (2026-10-23): `internal/api/authapi` serves `/sanctum/csrf-cookie`, `/auth/login`, `/auth/login/checkpoint`, `/auth/logout`, `/auth/password` and
`/auth/password/reset`, in the order the notes 03c and 03d lay out, and is tested end to end over HTTP with in-memory stores. The SQL behind the stores waits for the schema.

## Done
| Where | Content |
|---|---|
| `authapi.New(Deps)`, `Handlers.Register(group)` | The dependencies are checked (every store, the session manager, the CSRF protector, the failure tracker, four limiters, a logger). Each route is registered with its permission (guest, public, authenticated) and its per-address limiter. |
| `CSRFCookie` | Makes a guest session when there is none, and issues the token for it (the frontend calls this first, as it did with Sanctum). |
| `Login`, `Checkpoint`, `Logout` | The flow of 03d. A password is checked only for a user that was found, and the time of a check is spent for one that was not. Counters per account and client, per account and per client (`login.Counters`), kept by the new `middleware.Tracker`. The second step: the pending login with its five-minute and five-failure limits, a recovery token (spent with a conditional delete) or a code (accepted once: of a later step than the last, stored with a conditional update). A new session at every sign-in. |
| `ForgotPassword`, `ResetPassword` | The flow of 03c: the same answer whatever the address, one link a minute, at most 5 an hour to one address, the token stored as a hash and spent with a conditional delete, all sessions ended, nodes told (best effort), a user without two-factor signed in. |
| `middleware.Tracker` | Counts failures against counters with different limits and windows, fails closed for a counter that cannot work. In memory, per process. |
| `apierr.Display` | The 400 `DisplayException` of the reference, for refusals meant for the person. |
| `activity.WithRequestMetadata`, `MetadataKeys` | The address and browser of a request go into an entry's properties (`ip`, `useragent`), allowed on every event. |

## The interfaces the database layer fills
`Users` (`ByLogin`, `ByID`, `ByEmail`, `UseTOTPStep` as a **conditional update**, `SetPassword`), `RecoveryTokens` (`ForUser`, `Delete` that says whether the row was still there), `Resets` (`Latest`, `Replace`, `Consume` as a **conditional delete**),
`Outbox` (queue a mail), `Nodes` (`DeauthorizeUser`), `ActivityLog` (`Log`). The TOTP secret reaches `Login`/`Checkpoint` already opened by the store (`User.TOTPSecret`), and a user's last used step as `TOTPLastUsed`.
The three conditional operations are what keep two requests that carry the same code, recovery token or reset token from both succeeding: test them against MariaDB when it is there.

## What differs from the reference (`docs/compat.md`: C-053; C-038, C-039 earlier)
Failed sign-ins are logged only for a user that exists and without the name that was typed, and the three single-use things above are single-use under concurrency.

## Still to do (waits for the schema)
- The stores (sqlc), the real `Outbox` (an SMTP sender with retries) and `Nodes` (`wings.Client.Deauthorize`), the activity writer, and the production `api.Authorizer` (session, client key, application key, node token).
- The page routes (`GET /auth/login`, `/auth/password`, `/auth/password/reset/{token}`) must give the browser a guest session and the XSRF cookie, as `CSRFCookie` does, so that the first POST works without the extra call.
- `Tracker` is per process: with more than one process, put the counters in the database.
- A check against a real MariaDB that the conditional statements really are one statement each.
