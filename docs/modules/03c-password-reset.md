# M3 groundwork – forgot and reset password

Status: **groundwork done** (2026-10-22): the rules of the flow, which need no database or mail server. The endpoints (`POST /auth/password`,
`POST /auth/password/reset`), the table of tokens, the mail and the sessions wait for the schema and the mail module.

## Done: `internal/auth/passwordreset`
| Function | Content |
|---|---|
| `NewToken(rand)` | A token: 32 random bytes in hex (64 characters). Only the SHA-256 hash is stored; the plain token goes in the e-mail. |
| `Redeem(record, presented, now)` | Is a presented token good for the stored record: right form, matching hash (constant time), at most an hour old, not from the future. One error, `ErrInvalidToken`, for every failure. |
| `Decide(userExists, lastTokenAt, now)` | `Send`, `SkipUnknown` (no such user) or `SkipRecent` (a link was made less than a minute ago). The answer to the person asking is the same for all three. |
| `Link(appURL, token, email)` | The address in the e-mail, `<app url>/auth/password/reset/<token>?email=<escaped>`. It takes the configured public address and nothing from the request. The frontend reads the address from the link, so it stays. |
| `CheckNewPassword(password, confirmation)` | The reference's messages: required, confirmed, at least 8 characters; and at most 72 bytes. |
| `EffectsOf(usesTOTP)` | What a successful reset sets in motion: end all sessions, drop the nodes' tokens, delete reset tokens, and sign the user in unless they use two-factor authentication. |
| limits | `RequestPerIPPerMinute` (2), `ResetPerIPPerMinute` (10), `RequestPerEmailPerHour` (5). |

## The handlers, in this order
**Request a link** (limited to 2 a minute per client address and 5 an hour per e-mail address): check the form of the address; find the user;
`Decide`. For `Send`: make a token, replace the user's older tokens, store the hash with the time, put the mail in the outbox (never send it in the request). For the
other two: log the attempt with the address and the client IP (the reference dispatches a `FailedPasswordReset` event). When a link is sent the reference
logs `auth:reset-password`; Raptor does too. **Answer the same way, with the same body, in all cases**; do about the same
work (make a token in every case, write only for a known user) so that the time says little.

**Reset** (limited to 10 a minute per client address): `CheckNewPassword`; find the user by address; `Redeem` against the stored record (a user that does not
exist gives `ErrInvalidToken` as well); store the new password hash; apply `EffectsOf(user.use_totp)`; log `auth:password-reset` (a name the reference has in its language file); answer
`{"success": true, "redirect_to": "/", "send_to_login": effects.SendToLogin}`.

## What matches the reference
The one-hour life, one token per user, one e-mail a minute per user, the same answer for a known and an unknown address, the signed-in-or-not rule for two-factor
users, the answer of the reset, the messages of the password rules, and the shape of the link.

## Deliberate differences (`docs/compat.md`: C-038)
A limit on the reset endpoint and on the e-mails per address, a link that cannot be steered by the request, the other sessions and the nodes' tokens ended by a
reset, and a SHA-256 hash of the token instead of a bcrypt one.

## Still to do (waits for the schema and the mail module)
- The endpoints, the token table (the old `password_resets` rows are ignored), the outbox for the mail, the templates ("Reset Password": the three lines of the
  reference and the button), and the activity entries.
- The session store must be able to end all sessions of one user (`session.Manager`), and the node deauthorization goes through the outbox (`wings.Client.Deauthorize`).
- A reset must also be refused for a disabled account, if Raptor gets such a notion.
