# M8 groundwork – writes of users, nodes and locations, and the lists of the Application API

Status: **groundwork done** (2026-10-23): the rules of the write endpoints for users, nodes and locations, how request bodies are read, and how the lists
are filtered, sorted and expanded. Nothing here touches a database or the network; the endpoints, the stores and the push of a node's configuration to
Wings wait for the schema.

## Done
| Package | Content |
|---|---|
| `internal/fielderr` | `fielderr.Error{Field, Message}`: a refusal that names the field. `users`, `nodes` and `servers` return it; one conversion in the handlers turns it into the 422 entry (`meta.source_field`). `servers.FieldError` is an alias. |
| `internal/api/field` | `String`, `Secret`, `Int`, `Bool`: the scalar values of a body, read the way the framework reads them (C-046). Each has a state: `Absent`, `Null`, `Valid`, `Invalid`. Decoding never fails; the validator reports the field. |
| `internal/api/validate` | `Sent(field, state)` (required) and `Type(field, state, kind, nullable)` for the new types. The texts of `Length`, `Matches`, `IntRange` and `Email` now follow the reference's `validation.php` ("The name must be between 1 and 191 characters."). |
| `internal/users` | `PrepareAccount` (POST: password optional, language from the list, `NeedsSetup`), `PlanUpdate` (PATCH: what to store and the `Effects`), `CheckCanDelete`, `ValidateExternalID`. The username is stored in lower case (the old `Prepare` did not). |
| `internal/nodes` | `Settings.Apply(Change)` (PATCH, keeps `daemon_base`), `ChangeLocation`, `CheckCanDeleteNode/Location/AllocationOfNode`; `Validate` and `ValidateLocation` name the field. |
| `internal/api/listquery` | `Parse(query, spec)` for filters, sorts and includes with the specs of users, nodes, locations, servers, allocations, nests, eggs and server databases; `Query.Where()` and `Query.OrderBy(tieBreaker)` build the SQL from names of the spec and arguments only (C-045). |
| `internal/api/apierr` | `InvalidFilter` and `InvalidSort`: 400 with the codes `InvalidFilterQuery` and `InvalidSortQuery`. |

## Handler orders
**Create a user** (`POST /api/application/users`): the key and its `users` write permission, decode into `field` types, `Sent` for `email`, `username`, `first_name`,
`last_name`, `Type` for all fields (`external_id` and `password` nullable), `users.PrepareAccount(in, locales.Names(), rand.Reader)`. One transaction: insert (a
violated unique key on `email`, `username` or `external_id` becomes a 422 "has already been taken" for that field), and for `NeedsSetup` a reset token
(`passwordreset.NewToken`). After the commit: the account-created mail, the entry `user:user.create`, 201 with the user and `meta.resource`.

**Update a user** (`PATCH …/users/{id}`): as above, but `PlanUpdate(current, change, languages)`; the unique checks ignore the row itself. In one transaction store the
update; for `Effects.PasswordChanged` do what `passwordreset.EffectsOf` says, for `EmailChanged` delete the reset tokens of the old address, for `AdminRevoked` delete the
user's application API keys. The key's user is checked for `root_admin` on every request, so a demotion also ends the use of the user's own keys at once.

**Delete a user:** count the servers the user owns, `users.CheckCanDelete` (400 with the reference's text), delete. The rows that depend on the user (keys, SSH keys,
recovery tokens, subuser rows) go with it; check the cascades against the schema when it is there.

**Create / update a node:** decode, `Sent` for name, location, host, scheme, the four sizes and the two ports; `Settings.Apply(Change)` (an update) or `Validate` (a create);
the location must exist (422 on `location_id`). `reset_secret` counts only as the JSON value `true`, as in the reference. A new node, or a reset, gets
`nodes.NewCredentials`; the token goes into `raptor_secrets` (C-002). After the update the new configuration is pushed to Wings; when Wings cannot be reached the change
stays stored and the answer says so, with the reference's text ("…you will need to manually update the configuration file (config.yml)…").

**Delete a node / location / allocation:** `CheckCanDeleteNode` (servers on it), `CheckCanDeleteLocation` (nodes in it), `CheckCanDeleteAllocationOfNode` (the allocation's server).

**A list:** `listquery.Parse(r.URL.Query(), listquery.Users)`, then `resp.PageParams`, then `SELECT … WHERE <fixed part> AND <Where()> <OrderBy("id")> LIMIT ? OFFSET ?`
with the arguments of `Where()`. The count for the pagination uses the same `WHERE`. For allocations the node is part of the fixed condition. An include is
rendered only when the key may read the included resource, otherwise it is left out, as in the reference.

## What matches the reference
The names of the filters, sorts and includes of every list, the 400 texts, the partial match, the required fields of a PATCH (it is a full replacement except for the
optional fields), the refusals to delete, the texts of the three refusals, the lower-case username, the setup mail for a user without a password.

## Deliberate differences (`docs/compat.md`: C-044, C-045, C-046)
A node update keeps the base path, a password change ends the sessions, a demotion removes the application keys, the token id filter is exact, lists end with the id,
filters are bounded, and numbers with a fraction are refused.

## Still to do (waits for the schema)
- Everything that is database or network: the stores, the unique-key mapping, the transaction, the push to Wings, the setup token and the mail outbox.
- The `include` renderers (which fields each included resource shows, and the permission of the key for it).
- `per_page` on the lists that the reference takes from the query without a cap is capped at 100 by `resp.PageParams` (C-035); say so in the API notes for clients.
