# M4 – the account routes

Status: **handlers done over storage interfaces** (2026-10-26): `internal/api/accountapi`. `GET /account` and `GET /account/activity` are in `clientapi`.

| Route | What it does |
|---|---|
| `PUT /account/email` | the current password, then the address; three changes a day; a duplicate is 422 |
| `PUT /account/password` | the current password, then the new one twice; the other sessions end (`session.Manager.RevokeOthers`), the nodes drop the user's tokens, reset links asked for before stop working (C-049) |
| `GET /account/two-factor` | a new secret and the address of the QR code (refused while on); stored sealed at once |
| `POST /account/two-factor` | code and password switch it on; ten recovery tokens, shown once, stored as hashes; the code that did it cannot sign in afterwards |
| `POST /account/two-factor/disable` | the password; secret and recovery tokens go too |
| `GET/POST /account/api-keys`, `DELETE …/{identifier}` | at most 25 account keys (counted under the lock), the secret shown once in `meta.secret_token` and kept as a hash, the list of addresses checked when the key is made |
| `GET/POST /account/ssh-keys`, `POST …/remove` | OpenSSH or PEM keys, no DSA, RSA of 2048 bits or more, stored as PEM with the SHA-256 fingerprint; removing an unknown fingerprint is not an error |

Every request that asks for the current password again is counted per user and client, per user and per client (`account.ConfirmCounters`): wrong passwords and wrong codes use the allowance up, and the right password then no longer helps until it is over (C-049). The two-factor routes are open to a user who must still switch it on (`WithoutTwoFactor`); everything else of the account is closed to them.

Entries: `user:account.email-changed|password-changed`, `user:two-factor.create|delete`, `user:api-key.create|delete`, `user:ssh-key.create|delete`.

Added with it: `session.Manager.RevokeOthers` and `Store.DeleteForUserExcept`.

Still to do: the store (sealing the secret, the recovery-token table, the key tables), the adapter for the nodes.
