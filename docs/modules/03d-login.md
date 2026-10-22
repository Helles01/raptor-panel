# M3 groundwork – signing in

Status: **groundwork done** (2026-10-22): the rules of the login and the second-factor step that need no database. The endpoints, the look-ups, the stored
recovery tokens and the TOTP secrets wait for the schema. Earlier pieces of M3 are in `03-login-session.md` (TOTP with replay protection, sessions,
CSRF) and `03c-password-reset.md`.

## Done
| Where | Content |
|---|---|
| `internal/auth/login` | `Field` (an `@` means e-mail, else user name), `Identifier` (trim and check), `Counters` (the three failure counters), `FirstFactor` (rejected, done, needs a second factor, and whether to burn the time of a check), `SecondFactor` (what was sent: code or recovery token, and its form), `SecondFactorFailure`, `ConfirmationFailure`, `NewRecoveryTokens` (10 of 10 letters and digits), and the four messages of the reference. |
| `internal/auth/lockout` | The counter type and the naming of a client in a counter key (address without port, IPv6 reduced to its /64, an unreadable address refused). `internal/sftp` now uses it too. |
| `internal/auth/session` | `MaxConfirmationFailures` (5), `FailConfirmation` (count a wrong second factor, cancel the pending login at the limit) and `ConfirmationPending` (is there a login waiting that has neither expired nor used up its failures). |

## The handlers, in this order
**Login** (`POST /auth/login`, behind the 10 a minute per address limit of the authentication endpoints):
1. Decode `user` and `password`; `Identifier`; one that is unusable is a failed login without a look-up.
2. `Counters(identifier, client address)` (`ErrNoClient`: a 500); any counter `Blocked` is a 429 with the retry time.
3. Look the user up by `Field`; check the password if found; `FirstFactor`. `burn` means `auth.VerifyUnknownUser(password)` before answering.
4. `Rejected`: count a failure on every counter, log `auth:fail`, answer `ErrFailed` (400).
5. `Done`: regenerate the session (`Manager.Login`), log `auth:success`, answer `{data: {complete: true, intended, user}}`.
6. `NeedsSecondFactor`: `StartConfirmation`, log `auth:checkpoint`, answer `{data: {complete: false, confirmation_token}}`.

**Checkpoint** (`POST /auth/login/checkpoint`):
1. `Counters("id:<pending user id>", client address)`, or `id:0` when nothing is pending; `Blocked` is a 429.
2. Not `ConfirmationPending`: count a failure, answer `ErrTokenExpired`. A token that does not match (`CheckConfirmation`): `FailConfirmation`, count, `ErrCheckpointFailed`.
3. `SecondFactor(code, recovery)`: an error is counted and answered (`ErrNoSecondFactor` is a 422, not counted).
4. A recovery token: compare with the user's stored hashes (the old rows are bcrypt, `$2y$`, which `auth.VerifyPassword` reads), delete the one that matches. A code:
   decrypt the secret and `totp.Verify` with the last used step; store the new step. Success signs in as above; log `auth:token` or `auth:recovery-token`.
5. Failure: `FailConfirmation`, count on every counter, answer `SecondFactorFailure(method)`.

## What matches the reference
The messages, the two-step flow with the password checked first, the "an @ means e-mail" rule, the shape of both answers, the five-minute pending login, recovery tokens that
are single use and win over a code, the ten tokens of ten characters, and a new session at sign-in.

## Deliberate differences (`docs/compat.md`: C-039, and C-013 for the session)
Counters per account and client instead of per client, a time-equalised answer for an unknown user, a pending login that is cancelled after five wrong second
factors, and strict forms for the code and the recovery token.

## Still to do (waits for the schema)
- The endpoints, the stores (users, recovery tokens, `totp_authenticated_at`), and the limiters (`middleware.Limiter.Blocked` / `Fail`, one per counter); a successful login
  does not clear counters (the windows run out), which is a small difference from the reference.
- A recovery token is checked against the stored hashes one by one, up to ten password-hash checks per attempt (the same in the reference). The counters bound that cost.
- The audit before cutover: the number of users with two-factor authentication and without recovery tokens, so that an administrator can reissue them.
