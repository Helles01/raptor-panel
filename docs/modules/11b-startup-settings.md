# M11 groundwork – a user's own changes to a server: startup, settings, reinstall

Status: **groundwork done** (2026-10-23): the rules of `PUT …/startup/variable`, `PUT …/settings/docker-image`, `POST …/settings/rename` and `POST …/settings/reinstall`,
without a database. The endpoints, the stores and the call to the node wait for the schema.

## Done: `internal/servers/settings.go`
| Function | Content |
|---|---|
| `PlanStartupEdit(vars, key, value)` | Finds the variable by its environment name; a variable that does not exist or that the user cannot see is `ErrNoSuchVariable` (the same answer for both), a read-only one `ErrReadOnlyVariable` (both 400, the reference's texts). The value is normalized (`eggrules.Normalize`) and checked against the variable's rules with the attribute `value`; each failing rule is one message in a `*ValueError` (a 422 entry on `value` per message). The result says what to store, the old and new value for the log and whether anything changed. Rules that cannot be applied are an error that is not the user's fault. |
| `PlanImageChange(current, eggImages, requested)` | The image must be one of the egg's images (a 422 on `docker_image`), and the server must still use one of them (400 `ErrImageLocked`: an image an administrator set is left alone). The request is checked before the lock, as in the reference. |
| `PlanRename(current, name, description)` | Name 1 to 191 characters without control characters, description at most 2000 (the same helpers as a new server); no description sent keeps the old, a blank one clears it. Says which of the two changed, for the two log entries. |
| `CheckCanReinstall(status, skipScripts)` | Refuses a reinstall that would run no script, except for a server in a failed or interrupted state (the reinstall report is its way out). |
| `State.ValidateReinstall()` | `State.Validate` with one exception: a server whose first installation failed may be reinstalled by its owner (C-048). |

## Handler orders
**Set a variable:** the server and the permission `startup.update`, `State.Validate`, decode `key` (`Sent`, string) and `value` (the key must be present; null and blank
mean none), `PlanStartupEdit`. Store `variable_value = Store` (empty string for none), then the startup command is built again (`servers` has the builder) and returned
in `meta.startup_command` and `meta.raw_startup_command`. Only when `Changed`, the entry `server:startup.edit` with `variable`, `old` and `new`.

**Change the image:** permission `startup.docker-image`, `State.Validate`, `PlanImageChange`; when `changed`, store it and write `server:startup.image`.

**Rename:** permission `settings.rename`, `State.Validate`, `PlanRename`; store both, then one entry for each of `NameChanged` (`server:settings.rename`) and `DescriptionChanged`
(`server:settings.description`).

**Reinstall:** permission `settings.reinstall`, `State.ValidateReinstall`, `CheckCanReinstall`, set the status to `installing` and tell the node, in one transaction;
`server:reinstall`, 202.

## What matches the reference
The texts of the four refusals, the order of the checks, what counts as a change, the rules of the egg variables (`eggrules`, C-037), the one-message-per-rule answer.

## Deliberate differences (`docs/compat.md`: C-048)
A failed first installation can be reinstalled by its owner; a startup value that stays as it is is not logged.

## Open decision (for M5, the activity endpoints)
The entry `server:startup.edit` stores the old and the new value of the variable, and the entry is shown to everybody who has `activity.read`, also to a subuser who may not read
the startup variables. A variable can hold a password or a token. The reference has the same property. Options: keep (as the reference), or show the two values only to
readers who also have `startup.read`. To be decided when the activity endpoint of a server is written.

## Still to do
Everything that is database or network, and the builder of the startup command for the response (exists in `internal/servers`).
