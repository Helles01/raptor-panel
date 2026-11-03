-- What the owner of a server does with its allocations, subusers, schedules and backups, what the scheduler runs, and the housekeeping jobs.

-- name: ServerAllocationInfo :one
SELECT allocation_id, allocation_limit
FROM servers
WHERE id = ?;

-- name: LockServerAllocationInfo :one
SELECT allocation_id, allocation_limit, node_id
FROM servers
WHERE id = ?
FOR UPDATE;

-- name: CountAllocationsOfServer :one
SELECT COUNT(*)
FROM allocations
WHERE server_id = ?;

-- name: AllocationIP :one
SELECT ip
FROM allocations
WHERE id = ?;

-- A free allocation on the server's address, at random as in the reference.
-- name: PickFreeAllocation :one
SELECT id
FROM allocations
WHERE node_id = ? AND ip = ? AND server_id IS NULL
ORDER BY RAND()
LIMIT 1
FOR UPDATE;

-- name: PortsOfNodeAddress :many
SELECT port
FROM allocations
WHERE node_id = ? AND ip = ?;

-- name: InsertAllocationFor :execlastid
INSERT INTO allocations (node_id, ip, port, server_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: AssignOneAllocation :execrows
UPDATE allocations
SET server_id = ?
WHERE id = ? AND server_id IS NULL;

-- name: LockAllocationOfServer :one
SELECT id
FROM allocations
WHERE id = ? AND server_id = ?
FOR UPDATE;

-- name: SetAllocationNotes :exec
UPDATE allocations
SET notes = ?
WHERE id = ?;

-- name: SetServerPrimaryAllocation :exec
UPDATE servers
SET allocation_id = ?, updated_at = ?
WHERE id = ?;

-- name: UserRefByEmail :one
SELECT id, uuid, username
FROM users
WHERE email = ?;

-- name: ServerNameOf :one
SELECT name
FROM servers
WHERE id = ?;

-- name: CountSubuserEntries :one
SELECT COUNT(*)
FROM subusers
WHERE server_id = ? AND user_id = ?;

-- name: InsertSubuser :exec
INSERT INTO subusers (user_id, server_id, permissions, created_at, updated_at)
VALUES (?, ?, ?, ?, ?);

-- name: LockSubuserEntries :many
SELECT id
FROM subusers
WHERE server_id = ? AND user_id = ?
FOR UPDATE;

-- name: UpdateSubuserPermissions :exec
UPDATE subusers
SET permissions = ?, updated_at = ?
WHERE server_id = ? AND user_id = ?;

-- name: DeleteSubuserEntries :exec
DELETE FROM subusers
WHERE server_id = ? AND user_id = ?;

-- name: InsertSchedule :execlastid
INSERT INTO schedules (server_id, name, cron_day_of_week, cron_month, cron_day_of_month, cron_hour, cron_minute, is_active, is_processing, only_when_online, next_run_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?);

-- name: LockScheduleOfServer :one
SELECT id
FROM schedules
WHERE id = ? AND server_id = ?
FOR UPDATE;

-- name: LockScheduleRow :one
SELECT id
FROM schedules
WHERE id = ?
FOR UPDATE;

-- name: UpdateSchedule :exec
UPDATE schedules
SET name = ?, cron_day_of_week = ?, cron_month = ?, cron_day_of_month = ?, cron_hour = ?, cron_minute = ?, is_active = ?, only_when_online = ?, next_run_at = ?, updated_at = ?
WHERE id = ?;

-- name: ClearScheduleProcessing :exec
UPDATE schedules
SET is_processing = 0
WHERE id = ?;

-- name: DeleteScheduleOfServer :execrows
DELETE FROM schedules
WHERE id = ? AND server_id = ?;

-- name: ServerBackupLimitOf :one
SELECT backup_limit
FROM servers
WHERE id = ?;

-- name: LockTasksOfSchedule :many
SELECT id, sequence_id
FROM tasks
WHERE schedule_id = ?
ORDER BY sequence_id, id
FOR UPDATE;

-- name: MakeRoomForTask :exec
UPDATE tasks
SET sequence_id = sequence_id + 1
WHERE schedule_id = ? AND sequence_id >= ?;

-- A task moves to an earlier place: the ones from there to just before its old place move one later.
-- name: MoveTasksLater :exec
UPDATE tasks
SET sequence_id = sequence_id + 1
WHERE schedule_id = ? AND sequence_id >= ? AND sequence_id < ?;

-- A task moves to a later place: the ones from just after its old place to there move one earlier.
-- name: MoveTasksEarlier :exec
UPDATE tasks
SET sequence_id = sequence_id - 1
WHERE schedule_id = ? AND sequence_id > ? AND sequence_id <= ?;

-- name: CloseGapAfter :exec
UPDATE tasks
SET sequence_id = sequence_id - 1
WHERE schedule_id = ? AND sequence_id > ?;

-- name: InsertTask :execlastid
INSERT INTO tasks (schedule_id, sequence_id, action, payload, time_offset, is_queued, continue_on_failure, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?);

-- name: UpdateTaskRow :exec
UPDATE tasks
SET action = ?, payload = ?, time_offset = ?, continue_on_failure = ?, sequence_id = ?, updated_at = ?
WHERE id = ? AND schedule_id = ?;

-- name: DeleteTaskRow :exec
DELETE FROM tasks
WHERE id = ? AND schedule_id = ?;

-- name: DueSchedules :many
SELECT s.id, s.server_id, s.name, s.cron_minute, s.cron_hour, s.cron_day_of_month, s.cron_month, s.cron_day_of_week, s.is_active, s.is_processing, s.only_when_online, s.next_run_at, sv.status
FROM schedules s
JOIN servers sv ON sv.id = s.server_id
WHERE s.is_active = 1 AND s.is_processing = 0 AND (sv.status IS NULL OR sv.status = '') AND s.next_run_at IS NOT NULL AND s.next_run_at <= ?
  AND EXISTS (SELECT 1 FROM tasks t WHERE t.schedule_id = s.id)
ORDER BY s.id;

-- name: ScheduleForRun :one
SELECT s.id, s.server_id, s.name, s.cron_minute, s.cron_hour, s.cron_day_of_month, s.cron_month, s.cron_day_of_week, s.is_active, s.is_processing, s.only_when_online, s.next_run_at, sv.status
FROM schedules s
JOIN servers sv ON sv.id = s.server_id
WHERE s.id = ?;

-- name: TasksForRun :many
SELECT id, sequence_id, action, payload, time_offset, continue_on_failure
FROM tasks
WHERE schedule_id = ?
ORDER BY sequence_id, id;

-- name: BeginScheduleRun :execrows
UPDATE schedules
SET is_processing = 1, next_run_at = ?, updated_at = ?
WHERE id = ? AND is_processing = 0;

-- name: MarkTaskQueued :exec
UPDATE tasks
SET is_queued = ?, updated_at = ?
WHERE id = ?;

-- name: FinishScheduleRun :exec
UPDATE schedules
SET is_processing = 0, last_run_at = ?, updated_at = ?
WHERE id = ?;

-- name: ScheduleProgress :one
SELECT s.is_active, sv.status
FROM schedules s
JOIN servers sv ON sv.id = s.server_id
WHERE s.id = ?;

-- name: ClearScheduleMarks :exec
UPDATE schedules
SET is_processing = 0
WHERE is_processing = 1;

-- name: ClearTaskMarks :exec
UPDATE tasks
SET is_queued = 0
WHERE is_queued = 1;

-- name: LockServerBackupLimit :one
SELECT backup_limit
FROM servers
WHERE id = ?
FOR UPDATE;

-- name: InsertBackup :execlastid
INSERT INTO backups (server_id, uuid, is_successful, is_locked, name, ignored_files, disk, bytes, created_at, updated_at)
VALUES (?, ?, 0, ?, ?, ?, ?, 0, ?, ?);

-- name: SoftDeleteBackup :execrows
UPDATE backups
SET deleted_at = ?, updated_at = ?
WHERE id = ? AND server_id = ? AND deleted_at IS NULL;

-- name: LockBackupOfServer :one
SELECT id
FROM backups
WHERE id = ? AND server_id = ? AND deleted_at IS NULL
FOR UPDATE;

-- name: SetBackupLocked :exec
UPDATE backups
SET is_locked = ?, updated_at = ?
WHERE id = ?;

-- name: BackupStartTimes :many
SELECT created_at
FROM backups
WHERE server_id = ? AND created_at >= ?
ORDER BY created_at;

-- name: FailOrphanBackups :execrows
UPDATE backups
SET is_successful = 0, completed_at = ?, updated_at = ?
WHERE completed_at IS NULL AND deleted_at IS NULL AND created_at < ?;

-- name: DeleteActivityChunk :execrows
DELETE FROM activity_logs
WHERE `timestamp` < ?
LIMIT 5000;

-- name: DeleteResetTokensBefore :execrows
DELETE FROM password_resets
WHERE created_at < ?;

-- A session is spent when it was last used too long ago or, by the creation time its payload carries (UTC, to the second), is too old.
-- name: DeleteSpentSessions :execrows
DELETE FROM sessions
WHERE last_activity < ? OR LEFT(JSON_VALUE(payload, '$.created_at'), 19) < ?;

-- name: NonFailedBackups :many
SELECT id, created_at, is_locked
FROM backups
WHERE server_id = ? AND deleted_at IS NULL AND (is_successful = 1 OR completed_at IS NULL)
ORDER BY created_at, id;
