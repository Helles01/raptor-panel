# M9 groundwork – details, startup, suspension and deletion of a server (Application API)

Status: **groundwork done** (2026-10-23): the rules of `PATCH …/servers/{id}/details`, `PATCH …/startup`, `POST …/suspend|unsuspend`, `POST …/reinstall` and `DELETE …/servers/{id}[/force]`, without a database
or a node. The creation and the build change are in `09a-server-rules.md`.

The routes over storage interfaces are described in `08d-application-server-endpoints.md` (servers, their databases and their includes).

## Done: `internal/servers/lifecycle.go` (and `field.Text`)
| Function | Content |
|---|---|
| `ValidateEnvironment(vars, values, admin)` | The values for an egg's variables, checked against their rules (`eggrules`) with the attribute "<name> variable". An administrator's request covers every variable and a value that was not sent counts as none, so required ones fail; a user's covers only the variables they may see and change. All problems are returned at once, as 422 entries on `environment.<NAME>`. Returns what to store for each variable. Used by creation, the startup update and the user's own edits. |
| `PlanDetails(current, change)` | Name and owner needed; external id and description keep their value when not sent and are cleared by an explicit empty value (C-050); says whether the owner changed and who it was. |
| `PlanStartup(current, change, vars)` | Startup command (at most 8192 bytes), egg, image (the allowed form; it need not be one of the egg's), `skip_scripts` as sent, the environment checked only when it is not empty (as in the reference). |
| `PlanSuspension(action, status, transferring)` | A server that is as asked is left alone; a transfer is a 409 with the reference's text; a server that is installing, restoring or has a failed installation cannot be suspended or released (C-050). |
| `ParseDeleteForce`, `DeleteMayContinue` | The last part of the delete route: nothing or `force`, anything else is a 404; a failure of the node stops the deletion unless it is forced, except a 404, which means the node does not have the server. |
| `ParseListScope` | See `05a`. |
| `field.Text` | A value that is text but may be sent as a number (the environment values): `"25"` and `25` are both "25"; a flag is invalid. |

## Handler orders
**Details:** the key and the `servers` write permission, decode (`Sent` for `name` and `user`; `external_id` nullable), `PlanDetails`; the new owner must exist (422 on `user`); one transaction stores it;
when `OwnerChanged` the node is told to forget `PreviousOwner`'s tokens and SFTP sessions. The new owner's old subuser row, if any, can stay.

**Startup:** decode `startup`, `egg`, `image`, `skip_scripts` (all needed) and `environment` (an object of `field.Text`; a nested value is invalid); the egg must exist (422 on `egg`); load its variables;
`PlanStartup`; in one transaction update the server (the egg's nest too) and store the values (`variable_value`, empty for none); the variables of the old egg stay stored but unused; then the node is told.

**Suspend / release:** load the server and whether it is being transferred; `PlanSuspension`; for `Noop` answer 204 at once; store the status, tell the node to sync, and if that fails put the old status back and answer the failure.

**Reinstall:** `CheckCanReinstall(status, skip_scripts)` (a server that skips its scripts gains nothing, unless it is in a failed state); status `installing`; the node reinstalls; 204.

**Delete:** `ParseDeleteForce(segment)` (404 when not ok); tell the node; on an error `DeleteMayContinue(force, status)`; then in one transaction each database is removed from its host (a failure stops it unless forced, in which case the row goes
anyway), the notes of the allocations are cleared and the server row is deleted. 204.

## What matches the reference
The rules and messages of the startup values, the order of the checks, the transfer refusal text, the empty environment, the node's 404, the effect of an owner change.

## Deliberate differences (`docs/compat.md`: C-050)
Not-sent fields are kept, a state is not lost by a suspension, a word other than `force` is not a route.

## Still to do (waits for the schema and a node)
The stores and the node calls; whether to remove the new owner's subuser row; the refusal of a reinstall of a suspended server is left as in the reference (the reinstall puts the server in the `installing` state, which ends the suspension).
