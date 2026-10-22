# M12 groundwork – cron expressions and task rules

Status: **groundwork done** (2026-10-22): the pure logic of schedules, which needs no database. The endpoints
(`/servers/{server}/schedules*`, `…/execute`) and the in-process scheduler wait for the schema, because they load
schedules and tasks, check the user's permissions on the server and write `next_run_at` and `last_run_at`.

## Done: `internal/schedules`
| File | Content |
|---|---|
| `cron.go` | `Parse(minute, hour, dayOfMonth, month, dayOfWeek)` and `Cron.Next(after)`: when a schedule runs next. |
| `task.go` | `Task.Validate` (action allow-list, payload rules, offset 0–900, sequence ≥ 1), `Task.Permission` (the permission an action needs), `CanRun` (may someone run all tasks of a schedule by hand), `ValidateName`, `PlaceTask` (the sequence number of a new task, and whether the later ones shift). |

### Cron semantics, from the reference's library
The reference uses `dragonmantank/cron-expression` v3.6.0 (`composer.lock`) and passes it the five fields joined with
spaces. Read from its source at that tag:
- When **both** day fields are given (neither `*` nor `?`), a day matches if **either** does. With one of them `*` or `?`,
  only the other counts. The other fields must all match.
- The next run is the first whole minute after the given time; seconds are dropped, and a run in the current minute is
  skipped. The search gives up after a bounded number of steps and the reference then reports the expression as invalid;
  Raptor searches ten years ahead and reports the same error text.
- Syntax: numbers, names (`JAN`–`DEC`, `MON`–`SUN`, case-insensitive), `*`, lists, ranges, steps (`*/n`, `a-b/n`), `?` in the
  day fields (not both), `L` and `nW` in the day of month, `nL` and `n#m` in the day of week (`n#m`: m from 1 to 5; a month
  with no m-th such day simply has no run). A step on a plain number (`5/15`) is invalid.
- `nW` is the weekday nearest to day n without leaving the month: a Saturday goes to the Friday before (the Monday after on
  the 1st), a Sunday to the Monday after (the Friday before on the last day). A day n the month does not have is no match.

**Not verified by running the library:** there is no PHP runtime here, so these semantics come from reading the library's
source at the pinned tag, not from comparing outputs. The audit on real data (below) is the check that closes this.

The tests cover each of these with dates that were checked against a calendar (99 % of the statements), and mutation checks
(OR becoming AND, a run in the current minute, skipped local times, the Sunday fold, steps up to the field end, the
occurrence arithmetic of `n#m` and `nL`).

### Task rules
- Actions `command`, `power`, `backup`; a power payload is `start`, `stop`, `restart` or `kill`; a command is required;
  a backup payload is the list of files to leave out, one per line. `time_offset` is 0–900 seconds.
- Permissions as the reference's `Task::permissionForAction`: `control.console`, `backup.create`, `control.start`,
  `control.stop` (for stop and kill), `control.restart`.
- `CanRun` is the decision recorded as C-001: running a schedule by hand needs `schedule.update` **and** the permission of
  every task in it. An unknown action has no permission and blocks the run (C-023).

## What matches the reference
Everything above that is not listed under the differences: the day-field OR rule, the minute-granular next run, the syntax
of `L`, `W` and `#`, the task actions and their permissions, the limits (offset 900, name 191 characters, ten tasks per
schedule by default: `DefaultTaskLimit`), and the way a new task gets its sequence number (`PlaceTask`: last by default,
an earlier place shifts the later tasks by one).

## Deliberate differences (`docs/compat.md`: C-022, C-023)
- Forms whose meaning is a library quirk are refused (C-022). Where the quirk could not be pinned down from the library
  source (the exact step that counts as "too large"), Raptor refuses rather than guesses.
- An unknown task action or signal blocks a manual run instead of passing (C-023).

## Still to do
- **The audit** (M17): parse every stored schedule with `Parse` and list the ones that fail, before cutover, so none stops
  running unnoticed. A schedule that does not parse must not be silently dropped or run at a guessed time.
- The panel's time zone: the reference computes next runs in `app.timezone` (default UTC). Raptor needs the same setting
  (a `app.timezone` configuration value, an IANA name), and `Next` must be called with a time in that location. UTC is
  recommended: a clock change makes a local hour vanish or repeat (see the tests for what Raptor does then).
- Handlers and the scheduler: the schedule and task limits (`DefaultTaskLimit`, configurable), "a backup task cannot be
  created when the server's backup limit is 0", `only_when_online`, `continue_on_failure`, the `is_processing` lock so
  a schedule never runs twice at once, the activity log entries, and the per-task permission check on create and update
  (`Task.Permission`, with `schedule.update`).
