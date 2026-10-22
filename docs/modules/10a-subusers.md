# M10 groundwork – subuser rules

Status: **groundwork done** (2026-10-22): the decisions behind the subuser endpoints (`/servers/{server}/users*`) that need
no database. The endpoints, the notification e-mail, and the revocation of tokens and SFTP access on Wings through the
outbox wait for the schema.

## Done
| Where | Content |
|---|---|
| `permissions.Normalize`, `permissions.CanAssign` | See `07a-server-config.md`: what is stored for a subuser (known permissions only, `websocket.connect` always), and the subset rule. |
| `servers.AuthorizeSubuserChange(SubuserChange{…})` | Three rules, in this order. (1) Nobody changes or removes **themselves** (`ErrSelfEdit`, answered with 403). (2) Nobody changes or removes a subuser who **holds more than they do** (`ErrTargetOutranks`, C-025: the project owner's decision, not in the reference). (3) A request may only hand on permissions the actor holds (`ErrCannotAssign`, the reference's text). Owners and administrators pass `["*"]` as `Held` and are bound only by rule 1. When adding, `TargetUUID` and `TargetHeld` are empty; for a removal `Requested` is nil. The UUIDs are compared ignoring case. |
| `servers.CheckNewSubuser(ownerID, userID, alreadySubuser)` | The two refusals of `SubuserCreationService`: the owner cannot be added, and a user cannot be added twice. The texts are the reference's. |
| `users.GenerateUsername(email, rand)` | The username of the account created when an unknown email address is added as a subuser (C-024). Always passes `users.Validate`. |

Mutation checks confirmed that removing the self-edit check, making it case-sensitive, skipping the subset check, skipping
or mis-aiming the outranking check, and swapping the order of the rules each fail a test. The wildcard shortcut in
`CanAssign` is covered by the admin-name case.

## What matches the reference
The self-edit rule, the subset rule with its exceptions for owners and administrators, the two refusals when adding, the
username recipe (local part, 64 characters, three random characters), and the stored permission list (`Normalize`).

## Deliberate differences (`docs/compat.md`: C-024, C-025)
- Generated usernames always pass the username rule (C-024).
- **Whom a subuser may manage** (C-025). In the reference the subset rule limits only what a subuser may hand on; a subuser
  with `user.update` or `user.delete` can lower the permissions of, or remove, another subuser who holds more than they do.
  Decided by the project owner (2026-10-22): a subuser may manage only subusers whose permissions are a subset of their own
  ("only what they hold themselves"). Owners and administrators are not limited.
  - What it means in practice: two subusers with equal permissions can manage each other; a subuser with fewer permissions
    cannot touch one with more.
  - A subuser entry that holds a permission name which no longer exists (from an older panel version) can be managed only by
    the owner or an administrator: Raptor fails closed. `raptor audit` (M17) should list such entries before cutover.

## Still to do (waits for the schema)
- The endpoints, the permission check of each route (`user.create`, `user.read`, `user.update`, `user.delete`), the
  transaction that creates the account and the subuser entry together, the retry when a generated username is taken, and the
  notification e-mail.
- Revoking a removed subuser's websocket tokens and SFTP access on the node through the outbox (`wings.Client.Deauthorize`), so
  access ends at once and not when a token expires (plan 10.2).
