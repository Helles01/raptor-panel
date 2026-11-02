# M9 – the allocation routes of a server

Status: **handlers done over storage interfaces** (2026-10-26): `internal/api/allocapi`.

| Route | Permission | What it does |
|---|---|---|
| `GET …/network/allocations` | `allocation.read` | the server's allocations, `is_default` marks the primary one |
| `POST …/network/allocations` | `allocation.create` | one more: a free allocation on the server's address or a new one with a random port of the configured range; the switch, the limit and the free port are checked by `nodes.CheckCanAddAllocation` and `nodes.PickPort` inside the store's transaction (the handler passes them as functions); 2 a minute per server (C-058) |
| `POST …/{allocation}` | `allocation.update` | the note, which must be sent (null clears it); logged only when it changes |
| `POST …/{allocation}/primary` | `allocation.update` | makes it the primary one |
| `DELETE …/{allocation}` | `allocation.delete` | gives it up: needs a limit, not the primary one; a conditional release, 409 if the allocation changed meanwhile |

The allocation is always looked up among the server's own (404 for another server's). Entries: `server:allocation.create|notes|primary|delete` with `ip:port`. The refusals of the rules are `DisplayException` (400).

Still to do: the store (locking the server's allocations while counting), the settings (the switch, `range_start`, `range_end`).
