# M8 / M11 groundwork – allocations

Status: **groundwork done** (2026-10-22): the rules for adding allocations to a node and for a server's own allocations. The endpoints (`/api/application/nodes/{node}/allocations`,
`/api/client/servers/{server}/network/allocations*`), the inserts and the selection queries wait for the schema.

## Done: `internal/nodes/allocations.go`
| Function | Content |
|---|---|
| `ExpandAllocations(NewAllocations{IP, Ports, Alias})` | The allocations to insert: the addresses (one, or an IPv4 network of /25 to /32 with every address) times the ports (ports and ranges, duplicates removed). Limits: ports above 1024 up to 65535, a range at most 1000, a request at most 20 000 (C-041). The reference's texts for the refusals. |
| `ParsePorts` | One port entry; also used for the ports of an automatic deployment. |
| `ValidateAlias`, `ValidateNotes` | Alias and the note a user writes on an allocation. |
| `CheckCanAddAllocation`, `CheckCanDeleteAllocation` | The checks before a user adds an allocation (feature on, limit not reached; no limit set means none) or removes one (a limit must be set, not the primary). The allocation must belong to the server: the lookup from the URL must search among the server's own, which the reference has a middleware for. |
| `PickPort(start, end, used, rand)` | A free port of the range the administrator set for automatic allocations, at random. |

`ids.RandomBelow` (unbiased, up to 2^31) is new and also used by `dbhosts`.

## What matches the reference
The floor (ports above 1024), the ceiling, the range limit of 1000, the /25 to /32 networks with every address (network and broadcast included), the texts, and the limit semantics (none set means none).

## Deliberate differences (`docs/compat.md`: C-041)
No DNS lookup, no IPv6 networks, backwards ranges checked at their real ends, a bound on one request, and an allow-list for the alias.

## Still to do (waits for the schema)
- The insert is `INSERT IGNORE` in one transaction. The client's add must lock the server's allocations and the free ones of its node (`SELECT … FOR UPDATE`), and set `server_id`
  conditionally (`WHERE server_id IS NULL`), checking the affected rows.
- The configuration of the feature (`allocations.enabled`, `range_start`, `range_end`), off by default.
- A node with allocations in use cannot be deleted and an allocation in use cannot be deleted (the reference has exceptions for both).
