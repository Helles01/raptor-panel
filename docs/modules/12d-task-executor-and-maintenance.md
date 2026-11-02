# The work the panel does on its own: scheduled tasks, scheduled backups, housekeeping

Status: **done over store interfaces** (2026-11-02): `internal/taskexec`, `internal/api/backupapi/service.go` and `internal/maintenance`.
The stores (the tables, the conditional updates) wait for the schema; the processes that start these loops wait for `raptor serve`.

## A backup is started in one place: `backupapi.Service`
The endpoint of a user and the scheduler both start backups through `Service.Start`. Counting, the throttle and the insert happen in one
store transaction with the server's backups locked (plan 10.1 d); the node is told after the commit, and the row is taken back if it
refuses. What is new is **replacing**: a schedule's backup passes `Replace`, and a server at its limit then loses its oldest backup that
is not locked.

- The store's `Start` now returns `Started{Record, Pruned}`; the `Check` closure returns a `backups.Admission`, and if it says `Prune` the
  store deletes that row in the same transaction.
- After the commit the archive of the replaced backup is deleted on the node (a refusal is logged; a node that does not have it is no
  hindrance), then the new backup is made.
- Without `Replace`, or with every backup locked, or without a limit, the answer is the reference's "too many backups" (400).
- The throttle (2 in 600 s) applies to schedules too, as in the reference (C-068).

## `internal/taskexec`
`Executor` is the `schedules.Executor` of the runner: `State` (asks the node), `Power`, `Command`, `Backup` (through the Service, with
the name `Backup at <time>` in the panel's zone and `Replace`). What the node does wrong is wrapped with `schedules.ErrNode`, with the
cause kept, so a task with "continue on failure" goes on; what goes wrong on the panel (no room, the throttle, a server without a node,
the store) ends the run. `Starter` starts a run by hand for the schedule endpoint with the context of the process, so the run goes on
after the request; `SystemClock` is the clock of the runner. The adapter for the node is `controlapi.NewDaemon`.

## `internal/maintenance`
`Scheduler` runs jobs, each in a loop of its own (so a job never overlaps with itself and a slow one holds back no other): once at start
and then every interval; a failing or panicking job is logged and tried again; a stopped context ends them all.

| Job | Every | Does |
|---|---|---|
| `PruneOrphanedBackups(store, age)` | 30 min | marks backups that never reported (no end time) and are older than `age` (default 360 min) as failed; `FailOrphans` leaves finished ones alone |
| `PruneActivity(store, keep)` | day | deletes activity older than `keep` (default 90 days) |
| `PruneResetTokens(store)` | hour | deletes reset tokens older than their hour |
| `PruneSessions(store, idle, lifetime)` | hour | deletes sessions past the idle timeout or the lifetime (they are refused anyway; this is the size of the table) |

The wiring adds the first two only when their setting is not 0, as the reference does with `prune_age` and `prune_days`. Compat C-069.

## Tests
`taskexec` 98 %, `maintenance` 100 %, `backupapi` as before plus the service. Mutation spot checks killed every mutant (the replace flag,
the pruned archive, the compensation, the throttle, the cutoffs of every job, the intervals, panics, the node mark of an error).
