# M12 – the schedule routes

Status: **handlers done over storage interfaces** (2026-10-26): `internal/api/scheduleapi`.

| Route | Permission | What it does |
|---|---|---|
| `GET …/schedules`, `GET …/schedules/{id}` | `schedule.read` | `server_schedule` with its tasks (`schedule_task`), times in the panel's zone |
| `POST …/schedules` | `schedule.create` | `schedules.PlanCreate`: a flag not sent is off (C-052); a cron expression that never runs is 400; 2 a minute per server (C-058) |
| `POST …/schedules/{id}` | `schedule.update` | `schedules.PlanUpdate`: flags not sent keep their value; switching on or off clears a stuck running mark |
| `POST …/schedules/{id}/execute` | `schedule.update` and the permission of every task (C-001) | starts a run through `Starter`, which keeps the process's context (the run outlives the request); 202; already running is 409 (C-059) |
| `DELETE …/schedules/{id}` | `schedule.delete` | |
| `POST …/{id}/tasks` | `schedule.update` and the permission of the task's action | `action`, `payload`, `time_offset` 0-900, `sequence_id`, `continue_on_failure`; at most 10 tasks (counted again under the lock); a backup task needs a backup limit other than 0 |
| `POST …/{id}/tasks/{task}`, `DELETE …/{id}/tasks/{task}` | the same | the task moves in the sequence, the others make room |

The permission of a task's action is asked after the body is read and before it is validated (an action that is not one has none and is left to the validation). A schedule or task of another server or schedule is 404.

Entries: `server:schedule.create|update|execute|delete`, `server:task.create|update|delete` (C-059).

Still to do: the store (the sequence shifts and the task count in one transaction), and the `Starter` that calls `Runner.Execute` with the process's context.
