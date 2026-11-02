# M11 – the startup and settings routes

Status: **handlers done over storage interfaces** (2026-10-26): `internal/api/settingsapi`.

| Route | Permission | What it does |
|---|---|---|
| `GET …/startup` | `startup.read` | the variables a user may see (`egg_variable`), `meta`: `startup_command` (values of hidden variables as `[hidden]`), `docker_images` (an object in the egg's order), `raw_startup_command` |
| `PUT …/startup/variable` | `startup.update` | sets one variable, `key` and `value` (null and blank clear it); the egg's rules are checked, one 422 entry per broken rule; a variable the user cannot see is "does not exist" |
| `POST …/settings/rename` | `settings.rename` | name, and description if sent (null clears it); one entry for each that changed |
| `PUT …/settings/docker-image` | `startup.docker-image` | one of the egg's images, for a server that still uses one of them |
| `POST …/settings/reinstall` | `settings.reinstall`, the reinstall state rule (C-048) | marks the server installing (a conditional update) and tells the node; if the node cannot be told the mark is taken back |

Entries: `server:startup.edit` (old and new value), `server:settings.rename|description`, `server:startup.image`, `server:reinstall`. A value that stays as it was is not logged.

The open point of 11b stays open: `server:startup.edit` shows the values to everyone with `activity.read`.

Still to do: the store, and the adapter that tells the node (`wings.Client.Reinstall` exists).
