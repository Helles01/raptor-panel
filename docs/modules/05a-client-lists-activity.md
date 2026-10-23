# M5 groundwork – the client's lists and how an activity entry is shown

Status: **groundwork done** (2026-10-23): the server list's scope and search, the paging of the lists, and the form of an activity entry for the person who reads it.

## Done
| Where | Content |
|---|---|
| `servers.ParseListScope(type, admin)` | `?type=` of the server list: `owner`, `admin`, `admin-all`; anything else is the servers the user owns or is a subuser of. An administrator's list asked by a non-admin is empty (`ScopeNone`), not an error. |
| `listquery.ClientServers` | Filters `uuid`, `name`, `description`, `external_id` (partial) and `*`, the search: an IPv4 address with or without a port, or `:port`, is looked for in the allocations (equal address, port that begins so); any other text in the name (contains), UUID (equal or begins so), short UUID and external id (equal). `Where()` builds it with a subquery and arguments only. |
| `listquery.ClientActivity` | Filter `event` (partial), sort `timestamp`; the includes of the lists are `actor`. |
| `resp.PageParamsWith(q, default, max)` | The page sizes of the reference: the servers 50 of 100, the activity lists 25 of 100, the backups 20 of 50. |
| `activity.Present(entry, viewer, holders, loc)` | The attributes of an entry (C-051): the key `id` (SHA-1 of the row number, as in the reference), `batch`, `event`, `is_api`, `ip`, `description`, `properties`, `has_additional_metadata`, `timestamp` (in the panel's zone, with `+00:00`). |
| `activity.ParseTexts(json)` | Reads the activity texts of the frontend for `holders`: which properties an event's text already shows. An event whose texts are only singular and plural has none, so its entries always offer the extra metadata (the list of files). |
| `activity.HiddenEvents()` | `server:file.upload`, left out of every list. |

## The rules of `Present`
- **Address:** `ip` is shown to the user who did it and to administrators, and to nobody else (null). An address among the properties (`properties.ip`) is shown to the user it belongs to only, administrators included, otherwise `[hidden]`.
- **Properties:** a list or object also gets `<name>_count`; when exactly one such count exists it is called `count`. A `directory` is written with a slash at both ends.
- **Metadata:** an entry has more to show when a property is not one the text fills in and not the address, the browser or `using_sftp`.

## Handler orders
**The server list:** the user; `ParseListScope(type, root_admin)`; `listquery.Parse(query, ClientServers)` (a filter that is not offered is 400); `PageParamsWith(q, 50, 100)`; the query is the fixed condition of the scope
(`owner_id = ?`; the ids the user may reach; not those, for `admin`; nothing, for `ScopeNone`) joined with `Where()` and `OrderBy("id")`.

**The activity lists** (`GET /account/activity`, `GET /servers/{server}/activity`): the entries about the user, or of the server (the permission `activity.read`), without `HiddenEvents()`, `Where()` for `event`, `OrderBy("id")`
(the frontend asks for `sort=-timestamp`; without a sort the order is by id, and ties always end with it), `PageParamsWith(q, 25, 100)`; every row through `Present(…, Viewer{ID, root_admin}, holders, location)` with `holders` made once at start from `ParseTexts`.
With `activity.hide_admin_activity` the server list leaves out the entries of administrators who are neither the owner nor a subuser of the server: the reference joins the users and filters; do the same
with parameters. An included `actor` is the user item of the client API.

## Deliberate differences (`docs/compat.md`: C-051, C-035)
Plain characters in the search, no split at commas, a refused `per_page` rather than a cut one.

## Still to do (waits for the schema)
The queries, the actor include, the account details endpoint, and the writer of entries.
