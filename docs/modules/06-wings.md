# M6 – Power and Wings client

Status: **groundwork done** (2026-10-22): the Wings side of the module, which needs no database. The endpoints
(`POST /power`, `/command`, `GET /resources`, `/websocket`, `/files/upload`, `/files/download`, the backup download)
wait for the schema, because they look up the server, its node and the user's permissions. Both pieces are tested on
their own, including the checks that guard against the known bugs.

## Done: `internal/wings`
| File | Content |
|---|---|
| `node.go` | `Node` (scheme, host, port, decrypted token), validation (the token must be at least 32 characters), the URLs a browser needs (`WebsocketURL`, `UploadURL`, `FileDownloadURL`, `BackupDownloadURL`). `String`, `GoString` and `LogValue` leave the token out. |
| `jwt.go` | `Issuer` with one function per kind of token: `Websocket` (10 min), `FileUpload`, `FileDownload`, `BackupDownload` (15 min). HS256, signed with the node token. Claims as Wings v1.13.4 reads them (checked against its `router/tokens` source): `iss`, `aud`, `jti`, `iat`, `nbf`, `exp`, `scope`, `user_uuid`, `unique_id`, `server_uuid`, plus `permissions`, `file_path` or `backup_uuid`. |
| `client.go` | `Factory` (shared connections) and `Client` per node: `Power` (start, stop, restart, kill), `Command`, `Details`, `Deauthorize`. Errors become `*wings.Error` with the messages and statuses of the reference's `DaemonConnectionException` (504 when unreachable, the node's own status for 4xx with its message, a generic text for 5xx). |

## Known bugs covered (plan 10.1 a, 10.2)
- **GHSA-8r6w-3qq5-4p4r (JWTs without a purpose).** There is no function that builds a token without a scope, and each
  issuing function checks the permission of its scope **before** signing: `websocket.connect`, `file.create`,
  `file.read-content`, `backup.download` (`Subject.Can`). A subuser with only `websocket.connect` cannot obtain an upload
  token. Each token carries exactly one scope. A mutation check confirmed that removing the permission check fails the
  tests. Wings itself must still enforce the scope on the receiving side (Wings ≥ 1.12.2; the pinned version is 1.13.4):
  that is covered by the negative end-to-end tests in M7/M17.
- **Token reuse:** every token has a random `unique_id` (Wings accepts a unique token once), and the `jti` is the same for
  one user and server, which is what a revocation matches.
- **GHSA-grp4-3hv5-m6vf (signed URLs outliving a revocation):** short lifetimes as in the reference, and `Deauthorize`
  (`POST /api/deauthorize-user`) for the revocation, to be driven by the outbox in M10.
- **Secrets:** the node token travels only in the `Authorization` header, never in a URL, and no log or error string
  contains it. Redirects are **not followed** (the token would go to the redirect target), proxies from the environment
  are ignored (they would see the token), TLS 1.2 or newer, replies are limited to 1 MiB, inputs are validated before any
  request (UUIDs, the power action allow-list, command length and NUL bytes). Mutation checks confirmed that following
  redirects fails the tests.

## What matches the reference
The endpoints and bodies of the calls (`/api/servers/{uuid}/power` with `{"action"}`, `/commands` with `{"commands":[…]}`,
`/api/servers/{uuid}` for the live state, `/api/deauthorize-user`), the token lifetimes and the `nbf` skew of 5 minutes,
the `jti` (SHA-256 of the user id and the server UUID), the user-facing error messages, and the 15 s / 5 s timeouts
(`wings.timeout_seconds`, `wings.connect_timeout_seconds`).

## Deliberate differences (`docs/compat.md`: C-016 to C-018)
- TLS certificates are verified unless `wings.insecure_tls` is set (the reference verifies only in production).
- Redirects, proxies and oversized replies are refused (C-017).
- The tokens have no non-standard header fields (the reference also puts `jti` and `sub` into the JWT header, which Wings
  does not read) (C-018).

## Configuration added
`app.url` (`RAPTOR_APP_URL`: the issuer of the tokens, the Wings `remote`, the allowed browser origin) and the `wings`
section (`timeout_seconds`, `connect_timeout_seconds`, `insecure_tls` / `RAPTOR_WINGS_INSECURE_TLS`).

## Still to do in M6 (waits for the schema)
- Resolve the node and the user's permissions for a server (M5), implement `wings.Subject` on that, and write the
  handlers. The resources reply is cached for 20 seconds per server in the reference; add that in the handler.
- Activity log entries for power actions and commands.
- A real Wings (v1.13.4, WSL2) end-to-end run: the token contract above is checked against Wings' source, not yet against
  a running daemon.
