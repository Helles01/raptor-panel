# M7 groundwork – what a node reports: installation result and activity

Status: **groundwork done** (2026-10-22): the rules behind `POST /api/remote/servers/{uuid}/install` and `POST /api/remote/activity`.
The endpoints and their lookups (the server of the calling node only, the users) wait for the schema.

## Done
| Where | Content |
|---|---|
| `servers.ApplyInstallReport(status, installedBefore, report, notify)` | What a report from a node does: the new status (normal, `install_failed`, `reinstall_failed`, or `suspended` kept), that the time is stored, and which e-mail to send. Refuses a server that is not waiting for a result (C-033). The status names are now constants (`servers.StatusInstalling` …). |
| `activity.Process(body, now)` | Reads `{"data": […]}` and returns the entries to store and how many events were skipped (C-034). `Normalize` is the same for events already decoded. |

## What matches the reference
The new status and the suspended rule, `installed_at` set by every report, the "first installation or reinstallation" notion for the two
e-mail switches, the `server:` prefix rule, the default address `127.0.0.1`, the time zone handling (UTC), the fallback to the time of arrival
with `original_timestamp`, and that events for servers that are not the node's are silently left out (the handler's lookup).

## Deliberate differences (`docs/compat.md`: C-033, C-034)
A report is taken only from a server that is waiting for it, no "installed" e-mail for failures, and an activity request that survives a bad event.
Also: an empty set of properties is stored as `{}` (the reference writes `[]`, PHP's empty array); this is not a difference the interface sees.

## The handlers, in this order
Installation result: decode `successful` and `reinstall` (booleans; `successful` must be present), find the server, **check that it belongs to the
calling node** (structural, M7), `ApplyInstallReport`, store the status and `installed_at` in one update, send the e-mail the outcome asks for.
Activity: `Process`, look up the node's servers by the UUIDs in the entries (an entry for any other server is dropped) and the users, write the entries
and their subject rows in one transaction, log the number skipped. Answer 204 whatever was skipped.

## Still to do (waits for the schema)
- The two handlers, and the e-mail (needs the mail module: a template for "server installed" and one for "server reinstalled").
- **An accepted risk to review:** the node chooses the user an activity entry is attributed to (any user UUID it names), as in the reference, so a
  compromised node can write entries into any user's personal activity list. It could do more harm with the credentials it already holds, so the
  rule is kept; a stricter one would attribute only to users with access to the server.
- Check against a real Wings v1.13.4: the exact body it sends, that it accepts a 204, and that it no longer repeats events after a response in which
  some were skipped.
