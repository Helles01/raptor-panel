# 23 — Replacing secrets (M17, first part: the rotation commands)

Written 2026-11-08. Plan chapter 13.3 lists what has to be replaced at the cutover, because the old `APP_KEY` is assumed to be known to someone else. This note covers the two
commands of that table that were not there yet and that need nothing the owner has still to decide: the token of a node and the API keys. The rest of the table is listed
at the end, with what each of those needs.

## What was added
| Command | What it does |
|---|---|
| `raptor node rotate-token <id>` | Gives the node a new token id and a new token, sealed in `raptor_secrets` (`nodes`, id, `daemon_token`); the id in `nodes.daemon_token_id` changes in the same transaction. The new `config.yml` of the node, token included, is on the standard output, once; what the command says about itself is on the standard error. |
| `raptor apikey revoke --all [--kind account\|application] --yes` | Removes every API key (or every key of one kind) with the hashes and secrets sealed for them. Without `--yes` nothing is removed and the command says how many keys would go. |

`raptor apikey revoke <identifier>` (one key) is as it was; `--kind` and `--yes` belong to `--all` only, and the two forms do not mix (a usage error).

## How it works
- `internal/admin/rotation.go`: `Rotation.RotateNodeToken` reads the node (`NodeConfig`, without a token: a node that came with the old database and has no sealed token yet can be
  rotated too, which is the way out of its old token), makes new credentials, **renders the file before anything is stored** (a node whose file cannot be made keeps the token it
  has), then `ReplaceNodeToken`. If the store refuses, no file is given: it would hold a token that is not the node's.
- `internal/admin/directory.go`: `Directory.RevokeKeys` with `NotConfirmedError{Count}`; the kind is `account`, `application` or empty.
- `internal/store/admin.go`: `AdminRotation` (`NodeConfig`, `ReplaceNodeToken`: the node row is locked, the token id and the sealed token change in one transaction, so the id the
  authorizer looks up and the token it checks never disagree) and `adminDirectory.DeleteKeys` (the sealed secrets of the keys first, then the keys, in one transaction; four
  new queries in `queries/admin.sql`).
- `internal/admincli/more.go`: the two commands; `parseRange` lets a command take an optional argument. `cmd/raptor/cmd_admin.go` opens the `Rotation` part next to the others.

## Matched against the reference
- The reference changes a node's token in one place only: `PATCH /api/application/nodes/{id}` with `reset_secret: true` (`Node::resetDaemonToken`: a new id and a new token, the old
  ones gone). Raptor's Application API does the same and the new command is the same operation from the command line; it also works while the panel is down, which is when a
  token that has leaked is to be replaced.
- The reference has no way to remove all API keys at once; it is a cutover step of Raptor (plan 13.3), so there is nothing to match.

## What is intentionally different
- **`--yes` is required** for the bulk revoke and the command shows the number first. The other commands do not ask (they act on one thing named by the caller); this one acts on everything.
- The token of a node is **never printed by the command except inside the file**, and the file is only on the standard output; the standard error says "Gave node N a new token" and
  how to use the file, without the token or its id (a test checks it).
- A revoked key's owner has to make a new one; a message says so.

## What the node sees
The old token stops working at once. Until the new `config.yml` is on the node and Wings is restarted, the panel cannot reach the node (its servers keep running) and the node
cannot call the panel. Do it node by node, in a maintenance window (plan 13.3).

## Tests
- Unit: `internal/admin/rotation_test.go`, `directory_test.go`, `internal/admincli/more_test.go` (what goes to which stream, usage errors for every wrong form, nothing is changed
  when anything is refused). Nine mutants were made to fail on purpose (token not sealed, id not replaced, kind ignored, secrets of the kind kept, no preview, config error ignored,
  `--yes` ignored, kind forgotten in the command, message on the wrong stream): all were caught.
- Integration, on the real MariaDB (`internal/store/rotation_integration_test.go`): the old token no longer finds the node and the file that was given is the one the authorizer
  checks; the sealed value is replaced, not added to; a migrated node without a sealed token can be rotated; eight rotations at once leave the id and the token of the same one;
  a bulk revoke of one kind leaves the other kind and its sealed hash working, and the sealed secrets of other tables (a node's token, a second factor) are untouched.

## Still to do from the table of 13.3
| Step | What it needs |
|---|---|
| `raptor dbhost rotate-password <id>` | exists |
| `raptor dbhost rotate-all` (the passwords of the server databases) | a decision: passwords written into game server configurations break (plan 13.3 says to decide before the cutover) |
| `raptor user reset-2fa --all` with a "re-enrolment required" flag | a design: a table of Raptor's own, and a change of the sign-in so that the user enrols again after the password; `raptor user disable-2fa <user>` exists for one user |
| `raptor migrate-secrets --finalize` | confirmation and a fresh backup first; the command of the closing step (13.2, step 4) |
| `raptor audit` command (the check of the imported database, 13.5) | the owner's separate approval (the report itself is `internal/audit`) |
