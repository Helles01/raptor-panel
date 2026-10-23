# M7 groundwork – the events of the activity log

Status: **groundwork done** (2026-10-23): the catalog of the events the panel writes, the list of the events a node may report, and the log's form of a URL.

## Done: `internal/activity/events.go`
| Item | Content |
|---|---|
| Constants and `Specs` | Every event the panel itself writes (66 of them), with the properties that go into its entry. The names are the keys of the activity texts of the frontend; the properties are the placeholders of those texts. |
| `Properties(event, values)` | The JSON object to store. The event must be in the catalog and `values` must hold exactly its properties, so that no text on the screen has a hole and nothing is stored that no screen shows. An event without properties stores `{}`. |
| `DaemonEvent`, `DaemonEvents` | The eleven events Wings v1.13.4 sends: `server:power.start|stop|restart|kill`, `server:console.command`, `server:file.uploaded`, `server:sftp.write|create|create-directory|rename|delete`. Read from Wings' source (`server/activity.go`, `router/websocket`, `router/router_server_files.go`, `sftp/event.go`) with the version pinned in the plan. |
| `RedactURL` | The scheme, host and path of an http or https URL; the user name, password, query and fragment are dropped, and anything else gives an empty text. For `server:file.pull`. |
| `Normalize` | Takes only `DaemonEvent` names from a node. Before, it took any name that began with `server:`. |

## Tests that tie it to the frontend
`events_test.go` reads `internal/api/web/locales/en/activity.json` and checks that every event of the panel and of a node has a text, that no text uses a placeholder the
event does not carry, and that every text belongs to an event somebody writes (one old text, `server:file.upload`, is listed as known). When the frontend or the catalog
changes, one of them fails until the other is brought along. A node event may not be a panel event, so a node cannot forge one, except the five that both sides write by design (`server:power.start|stop|restart|kill` and `server:console.command`): the panel logs them for a call of the API (M6), a node for the console of the browser, and the test names exactly these five.

## What differs from the reference (`docs/compat.md`: C-047)
Nodes are limited to their own events, the panel writes `auth:password-reset` where the reference writes `event:password-reset`, a pulled URL is logged without credentials.
The reference writes `server.backup.restore-failed` (with a dot) in one place, `server:backup.restore-failed` in another; Raptor writes the second (C-027).

## Not in the catalog
The events of a node that the frontend has texts for but Wings v1.13.4 does not send are not accepted. If a later Wings sends more, add them to `daemonEvents` with a text.

## Still to do
The writer that stores an entry (subject, actor, request metadata) and the endpoints that read the log; the `withRequestMetadata` rule: only the events the reference marks with it
store the client's address and user agent. Check that rule against the reference when the writer is made.
