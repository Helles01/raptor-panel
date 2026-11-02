# M10 – the subuser routes

Status: **handlers done over storage interfaces** (2026-10-26): `internal/api/subuserapi`. The stores wait for the schema.

| Route | Permission | What it does |
|---|---|---|
| `GET /servers/{server}/users` | `user.read` | the subusers, `object: server_subuser` (the user object of `clientapi` and `permissions`) |
| `POST …/users` | `user.create` | adds a user by e-mail address; an address with no account gets one (C-024), a setup mail and an "added" mail; 10 additions in 15 minutes per server (C-058) |
| `GET …/users/{user}` | `user.read` | one subuser; `{user}` is the user's UUID |
| `POST …/users/{user}` | `user.update` | new permissions; the tokens and SFTP access are revoked in the same transaction, but only when they really change |
| `DELETE …/users/{user}` | `user.delete` | removes the subuser, revokes the same way, "removed" mail |

Order: the route's permission, then the rules of who may change whom (`servers.AuthorizeSubuserChange`: not oneself, not someone who holds more (C-025), nothing handed on that the caller does not hold), then the validation (the reference's order), then the work. The stored list is `permissions.Normalize` (known names only, `websocket.connect` always).

Adding: the address is looked up without regard to case; the owner and existing subusers are refused with the reference's texts (400); the generated user name is tried again up to five times if taken, and an address that got an account meanwhile is looked up again.

Entries: `server:subuser.create|update|delete`, with the user as subject. Mails and entries that fail are logged and do not undo the change.

Differences: C-058, C-059 (and C-024, C-025 from the rules).

Still to do: the store (the user and subuser tables, the outbox row for the revocation), the mails' delivery.
