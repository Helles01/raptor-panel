# M7 groundwork – SFTP sign-in, SSH keys, server state

Status: **groundwork done** (2026-10-22): the rules behind `POST /api/remote/sftp/auth`, which the node calls for every SFTP login.
The endpoint, the lookups (user, server on this node, registered keys) and the activity entries wait for the schema.

## Done
| Where | Content |
|---|---|
| `internal/sftp` | `Request.Method` (password unless the type says a public key; the key arrives in `password`), `ParseUsername` (`<user>.<8-character server id>`, split at the last dot), `Throttles`, `CheckKey`, `Authorize`, `Grant` (the answer: `user`, `server`, `permissions`). |
| `internal/sshkeys` | `Parse` an OpenSSH line or a PEM key (not DSA, RSA of at least 2048 bits, at most 16 KiB, nothing after the key), its `Fingerprint` (SHA-256, base64 without padding and without the `SHA256:` the interface adds itself), and `PEM()` for storing. Tested against keys made with `ssh-keygen`, whose `-l` output is the expected fingerprint. |
| `internal/servers` | `State.Validate`: a server that is suspended, on a node under maintenance, not installed, restoring a backup or being transferred is refused with the reference's message (409). Used here, and by every endpoint that needs a usable server. |
| `internal/permissions` | `Effective`: the permission list of a user on a server (administrator: everything and the `admin.*` ones; owner: everything; anyone else: their subuser entry). |
| `internal/auth` | `VerifyUnknownUser`: the time of a password check, for a user that does not exist. |
| `internal/api/middleware` | `Limiter.Blocked` and `Fail`: a limiter that counts failures only. |

## The handler, in this order
1. Decode the body; `Request.Method` (400 on a bad type or empty fields).
2. `ParseUsername`: `ErrBadServerID` is a 400, `ErrRejected` a 403 that counts as a failure.
3. For each of `Throttles(username, request.IP, node.ID)`: `Blocked` is a 429 with the retry time. Nothing is counted yet.
4. Find the user by name, and the server by its short id **on the calling node only** (the node scope of M7, structural). A user or
   server that is not found is a failure like any other: `auth.VerifyUnknownUser` for a missing user, then 403 with the same text.
5. A password: `auth.VerifyPassword`; failure: count on every throttle (`Fail`), log `auth:sftp.fail` with method password, 403.
   A key: `CheckKey` against the user's fingerprints; `KeyMatch` goes on, `KeyUnknown` is a 403 that is **not** counted (clients
   offer every key they have, and the node's SSH library asks for proof of the private key afterwards), `KeyInvalid` is a 403 that
   is counted.
6. `Authorize`: `ErrDenied` (403, log `server:sftp.denied`), or a `*servers.StateError` (409); otherwise answer with the `Grant`.

## What matches the reference
The request, the answer, the 403 and its text for every kind of failed sign-in, the rule that an offered but unregistered key is not
counted, the right needed (`file.sftp`, owners and administrators always), the order (right first, then state), the state messages,
the permission list of administrators, owners and subusers, and the fingerprint format.

## Deliberate differences (`docs/compat.md`: C-031, C-032)
Different counters (the reference's is keyed by the node's address, so one guesser blocks a whole server), a password check for a
user that does not exist, a strict form of the server id, and fewer key formats when *adding* a key.

## Still to do (waits for the schema and a real Wings)
- The endpoint, and the three limiters (`NewLimiter(limit, window)` per `Throttle`) held by the server process; their state is in memory,
  so a restart clears it (the same as the other limiters).
- Check against a real Wings v1.13.4: the `ip` it sends, the answer it expects, the 403 and 409 it reads, and that a client with
  several keys is not blocked by the counters (only unreadable keys and wrong passwords count).
- The rule for adding a key in the account endpoints (M4) uses `sshkeys.Parse`; the length of the `name`, duplicates by fingerprint.
