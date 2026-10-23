# M12 groundwork – the scheduler and the requests that change schedules

Status: **groundwork done** (2026-10-23): the rules of a run, the runner that carries it out against interfaces, and the checks of a request to create or change a schedule. The database store, the node
executor and the wiring into `raptor serve` wait for the schema and the Wings client's use.

## Done: `internal/schedules`
| Where | Content |
|---|---|
| `run.go` | `Due` (the query the scheduler makes), `PlanStart` (next time from now, first task), `First`/`NextTask`, `OnlineGate`, `BeforeTask`, `Classify`/`AfterTask` with `ErrNode`. All pure. |
| `runner.go` | `Runner` with `Store`, `Executor` and `Clock` interfaces: `Recover`, `Tick`, `Execute` (by hand), `Run(ctx, interval)`, `Wait`. A run is a chain of tasks, one goroutine each, with a bound on the actions against nodes. Tested with in-memory fakes and the race detector. |
| `request.go` | `PlanCreate`, `PlanUpdate`: name, the five cron fields, flags, next run time in the panel's zone, reset of the running mark when the schedule is switched on or off (C-052). `ValidateName` now returns a field error. |
| `Task` | gained `ID` and `ContinueOnFailure`. |

## How a run goes
1. **Tick** (every minute): `Store.Due(now)`. The SQL is: schedule active, not processing, server `status IS NULL`, `next_run_at <= now`, at least one task. For each schedule `PlanStart`, then `Store.Begin`:
   one transaction that sets `is_processing`, stores the new `next_run_at` and queues the first task, written as `UPDATE schedules SET … WHERE id = ? AND is_processing = 0` and **false when no row changed**.
2. With `only_when_online`, ask the node for the state: a run goes on only if it answered and the state is not `offline` or `stopping`. Otherwise it ends at once (task unqueued, `last_run_at` stored).
3. **Chain:** wait the task's offset (not for the first task of a run by hand); read the schedule's switch and the server's status again (`Store.Progress`) and decide with `BeforeTask`; do the action (`power`, `command`, `backup`); unqueue the task;
   `AfterTask`; read the tasks again and queue the next one (the lowest sequence above this one); at the end `Store.Finish` (not processing, `last_run_at`).
4. **Stop and crash:** when the process stops, the runs stop at their next wait and leave their marks. `Recover` runs once at start and clears `is_processing` and `is_queued` everywhere, since no run can be going on then.

## The interfaces the database layer and the node client fill
`Store`: `Due`, `Schedule`, `Tasks`, `Begin`, `Queue`, `Dequeue`, `Finish`, `Progress`, `ResetRunning`. `Executor`: `State`, `Power`, `Command`, `Backup`; every error that comes from the node (not reached, or an error answer)
must wrap `schedules.ErrNode`, so that `continue_on_failure` applies to it; the backup is also made by the panel (a row and the limit rules of `backups.Admit` with the override a scheduled backup uses, which prunes the oldest unlocked backup when the limit is reached), and a failure there is the panel's. `Clock`: the real one uses a timer that honours the context.

## Handler orders
**Create** (`POST …/schedules`, permission `schedule.create`): decode (`Sent` for `name` and the five fields, `Type` string; `is_active` and `only_when_online` are `field.Bool`, `is_active` not nullable), `PlanCreate(in, now, app.Location())`; an `ErrCron` is a 400 with the reference's text.
Store the row, write `server:schedule.create`.
**Update** (`POST …/schedules/{id}`, `schedule.update`): `PlanUpdate(current, in, …)`; store; when `ResetProcessing`, clear the mark; write `server:schedule.update` with `name` and `active`.
**Run by hand** (`POST …/schedules/{id}/execute`, `schedule.update` and the permission of every task, `CanRun`, C-001): `Runner.Execute`; `ErrAlreadyRunning` is a 409, `ErrNoTasks` a 400 (the reference's text); 202; write `server:schedule.execute`.
**Delete:** the tasks go with it; write `server:schedule.delete`. Tasks: `Task.Validate`, `PlaceTask`, the limit of tasks, and the permission of the action as before.

## What matches the reference
The state machine of a run, the texts, the selection of due schedules, the first-task and next-task order, what an offline server does, and that a schedule created without a flag is off.

## Deliberate differences (`docs/compat.md`: C-052; C-001, C-022, C-023 earlier)
Unsent flags are kept on an update, marks are cleared at start, runs cannot overlap, and the actions against nodes are bounded.

## Still to do (waits for the schema)
The `Store` (sqlc queries and the conditional `Begin`), the `Executor` over the Wings client and the backup service, the wiring (`Recover`, then `Run(ctx, time.Minute)` next to the HTTP server, stopped with it), the hashid of a schedule in the answers,
and a test of `Begin` against a real MariaDB (two runners racing for one schedule).
