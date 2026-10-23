# M4 groundwork – a user's own account: e-mail, password, two-factor authentication

Status: **groundwork done** (2026-10-23): the rules of `PUT /api/client/account/email`, `PUT …/account/password` and the four two-factor endpoints, without a database.
The endpoints, the stores and the sealing of the secret wait for the schema.

## Done: `internal/account`
| Function | Content |
|---|---|
| `ConfirmCounters(userID, clientAddr)` | Three failure counters for the requests that ask for the current password again, per user and client, per user, per client (C-049). A request whose client address cannot be read is refused (`login.ErrNoClient`). |
| `EmailChangeCounter(userUUID)` | The 3-in-24-hours counter of address changes, hit only when the address really changes. |
| `PlanEmailChange(current, requested, blocked)` | The limit is checked first (also for a request that changes nothing), then the address (`users.ValidateEmail`); the same address in another case is no change. |
| `PlanPasswordChange(currentOK, password, confirmation)` | A wrong current password is `ErrInvalidPassword` before any rule about the new one; then `passwordreset.CheckNewPassword` (twice, 8 characters, 72 bytes) as one `*PasswordError`; the hash and the three effects. |
| `PlanTwoFactorSetup(enabled, issuer, email, r)` | Refused while it is on (the secret in use cannot be replaced); otherwise a new 32-character secret and the `otpauth://` address. |
| `PlanTwoFactorEnable(passwordOK, code, secret, now, r)` | The order of the reference (shape of the code, password, then the code) with replay protection: the confirmed step is returned as the time of use. Ten recovery tokens and their hashes. |
| `PlanTwoFactorDisable(passwordOK)` | The password must be right; the secret and the recovery tokens are to be deleted. |

## Handler orders
**Change the e-mail address:** the user (a session or an account key), the counters of `ConfirmCounters` (a blocked one is 429), the current password (a wrong one counts in all three counters
and is 400), decode `email` (`Sent`, string), `PlanEmailChange(current, email, EmailChangeCounter blocked)`. When `Change`: hit the counter, store (a duplicate is a 422 on `email`), delete the reset
tokens of the old address, write `user:account.email-changed` with `old` and `new`. 204.

**Change the password:** the counters, `current_password` against the hash (a wrong one counts), `PlanPasswordChange`; store the hash; end the user's other sessions (all but the one of the request),
have the nodes drop the user's tokens, delete the reset tokens; write `user:account.password-changed`. 204.

**Two-factor setup** (`GET`): `PlanTwoFactorSetup(user.use_totp, app.name, email, rand.Reader)`; seal and store the secret now (an unconfirmed one is overwritten by the next request); answer
`{"data":{"image_url_data": URI, "secret": secret}}`.

**Two-factor enable** (`POST`): decode `code` and `password` (`Sent`, string); the counters; the password (a wrong one counts); the stored secret opened; `PlanTwoFactorEnable`. A wrong code counts too.
In one transaction delete the old recovery tokens, insert the hashes, set `use_totp` and `totp_authenticated_at = UsedAt`; write `user:two-factor.create`; answer the tokens
(`{"object":"recovery_tokens","attributes":{"tokens":[…]}}`).

**Two-factor disable** (`DELETE`): decode `password`; the counters; `PlanTwoFactorDisable`; clear `use_totp`, the secret and the recovery tokens; write `user:two-factor.delete`. 204.

## What matches the reference
The texts of all refusals, the 3-in-24-hours limit and what counts as a change, the order of the checks, the ten recovery tokens of ten characters stored as password hashes, the setup address.

## Deliberate differences (`docs/compat.md`: C-049)
Wrong passwords are counted, a password change reaches the nodes and the reset tokens, switching two-factor off removes what it left behind, the confirming code cannot be replayed.

## Still to do (waits for the schema)
- The session manager needs "end all sessions but this one" (`RevokeUser` ends all); add it with the store.
- The stores, the sealing of the secret (`raptor_secrets`), the recovery-token table and the activity writer; the account details endpoint and the SSH-key and API-key endpoints use the rules written earlier (`sshkeys`, `auth.keyaccess`).
