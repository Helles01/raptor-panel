# M10 groundwork – subuser rules

Status: **groundwork done** (2026-10-22): the decisions behind the subuser endpoints (`/servers/{server}/users*`) that need
no database. The endpoints, the notification e-mail, and the revocation of tokens and SFTP access on Wings through the
outbox wait for the schema.

## Done
| Where | Content |
|---|---|
| `permissions.Normalize`, `permissions.CanAssign` | See `07a-server-config.md`: what is stored for a subuser (known permissions only, `websocket.connect` always), and the subset rule. |
| `servers.AuthorizeSubuserChange(actorUUID, held, targetUUID, requested)` | The two checks the reference's `SubuserRequest::authorize` makes: nobody changes or removes **themselves** (`ErrSelfEdit`, answered with 403), and a request may only hand on permissions the actor holds (`ErrCannotAssign`, with the reference's text). Owners and administrators pass `["*"]` as `held`. For adding a subuser the target is empty; for a removal `requested` is nil. The UUIDs are compared ignoring case. |
| `servers.CheckNewSubuser(ownerID, userID, alreadySubuser)` | The two refusals of `SubuserCreationService`: the owner cannot be added, and a user cannot be added twice. The texts are the reference's. |
| `users.GenerateUsername(email, rand)` | The username of the account created when an unknown email address is added as a subuser (C-024). Always passes `users.Validate`. |

Mutation checks confirmed that removing the self-edit check, making it case-sensitive, skipping the subset check, and
swapping the owner/duplicate order each fail a test. The wildcard shortcut in `CanAssign` is covered by the admin-name case.

## What matches the reference
The self-edit rule, the subset rule with its exceptions for owners and administrators, the two refusals when adding, the
username recipe (local part, 64 characters, three random characters), and the stored permission list (`Normalize`).

## Deliberate differences
- Generated usernames always pass the username rule (C-024).

## Open question (not implemented, for the project owner to decide)
In the reference, the subset rule limits what a subuser may **hand on**. It does not limit **whom they may change**: a
subuser with `user.update` or `user.delete` can lower the permissions of, or remove, another subuser who holds more than they
do (they cannot gain anything by it). Raptor keeps this behaviour so that existing setups work the same. A stricter rule
would be "only manage subusers whose permissions are a subset of your own"; it would be a small addition to
`AuthorizeSubuserChange`, but it changes who can do what, so it is not done without a decision.

## Still to do (waits for the schema)
- The endpoints, the permission check of each route (`user.create`, `user.read`, `user.update`, `user.delete`), the
  transaction that creates the account and the subuser entry together, the retry when a generated username is taken, and the
  notification e-mail.
- Revoking a removed subuser's websocket tokens and SFTP access on the node through the outbox (`wings.Client.Deauthorize`), so
  access ends at once and not when a token expires (plan 10.2).
