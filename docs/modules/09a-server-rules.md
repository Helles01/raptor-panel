# M9 groundwork – creating and changing servers

Status: **groundwork done** (2026-10-22): the validation and the decisions of the Application API's server endpoints, with no database. The endpoints, the stores and the call
that creates the server on the node wait for the schema.

## Done: `internal/servers/create.go`
| Function | Content |
|---|---|
| `NewServer.Validate` | Name, description, owner, egg, image, startup command, external id, limits, feature limits and the number of additional allocations. The error is a `*FieldError` that names the field (`limits.memory`, `docker_image`, …) for `meta.source_field`. |
| `Limits.Validate`, `ValidateThreads`, `LimitsUpdate.Apply`, `FeatureLimits.Validate` | Limits within what the reference allows and the database holds; a CPU set that is one; a partial update applied and then checked as a whole. |
| `CheckNewServerAllocations` | Every allocation must exist, be free, be on the node, and be different (C-042). |
| `PlanAllocationChange` | What an add and remove of allocations does: what to assign (conditionally), what to release, and the new default, with the reference's two errors for a default that cannot be removed or named. |
| `NodeUsage.Fits`, `FindViableNodes` | Public nodes, optionally in the given locations, with room for the memory and disk including overallocation; -1 means no check. |
| `ParsePortSpecs`, `SelectAllocation` | The ports of an automatic deployment, and the random choice of a free allocation on the viable nodes, with or without a dedicated address. |

## The handlers, in this order
**Create** (`POST /api/application/servers`): decode; `NewServer.Validate`; the egg's variables with `eggrules`; for a deployment `FindViableNodes`, `ParsePortSpecs`,
`SelectAllocation`; then in **one transaction**: lock and read the allocations, `CheckNewServerAllocations`, insert the server, assign the allocations conditionally and refuse the
whole creation unless every row was updated, insert the variables. After the commit create the server on the node; if that fails, delete it again.

**Build change** (`PATCH …/build`): `LimitsUpdate.Apply`, `FeatureLimits.Validate`, `PlanAllocationChange` under a lock, apply, then tell the node to sync.

## What matches the reference
The names and limits of the fields, the order of the steps, the two errors of the allocation change, the node choice formula (without its -1 bug), and the notion of a dedicated address.

## Deliberate differences (`docs/compat.md`: C-042)
Allocations are checked and assigned conditionally, changes that cannot be done are errors, -1 overallocation means no check, bounded limits, real CPU sets, and stricter images and ports.

## Still to do (waits for the schema)
- Everything that is database: the locks, the conditional updates, the transaction, the hashids, the activity entries.
- The reference does not check capacity when an administrator names the node and the allocation (only an automatic deployment does). Raptor keeps that, so an administrator may
  overallocate on purpose; whether the answer should carry a warning when a node would be over its limit is a decision for the API design.
- Which Docker image a server may have: the reference checks only the form of it. Clients may later choose only among the egg's images (that rule is for M11).
