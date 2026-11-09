package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Helles01/raptor-panel/internal/audit"
	"github.com/Helles01/raptor-panel/internal/nodes"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/settings"
)

// AuditSource reads the rows that the check of the stored data looks at (internal/audit). It only reads, with fixed statements, and reads all of them in one
// read-only transaction, so that the report is about one moment and not a mixture of several.
type AuditSource struct{ db *sql.DB }

// NewAuditSource returns an AuditSource over db.
func NewAuditSource(db *sql.DB) *AuditSource { return &AuditSource{db: db} }

// unreadable stands in for a stored list that is not a list of texts: the checks then find that it cannot be read, which is what they are for, and the audit
// does not stop at the one row.
const unreadable = "(not a list of texts)"

// Load reads every kind of row.
func (a *AuditSource) Load(ctx context.Context) (audit.Input, error) {
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return audit.Input{}, fmt.Errorf("begin a read-only transaction: %w", err)
	}
	defer tx.Rollback()
	var in audit.Input
	for _, step := range []struct {
		what string
		load func(context.Context, *sql.Tx, *audit.Input) error
	}{
		{"egg variables", loadEggVariables}, {"egg images", loadEggImages}, {"schedules", loadSchedules}, {"API keys", loadAPIKeys}, {"subusers", loadSubusers},
		{"servers", loadServers}, {"nodes", loadNodes}, {"locations", loadLocations}, {"allocations", loadAllocations}, {"users", loadUsers},
		{"recovery tokens", loadRecoveryTokens}, {"settings", loadSettings},
	} {
		if err := step.load(ctx, tx, &in); err != nil {
			return audit.Input{}, fmt.Errorf("read the %s: %w", step.what, err)
		}
	}
	return in, nil
}

// each runs a query and hands every row to scan.
func each(ctx context.Context, tx *sql.Tx, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// textList reads a stored JSON list of texts. A column that is empty is an empty list; one that is anything else than a list of texts is one entry that cannot be read.
func textList(s sql.NullString) []string {
	if !s.Valid || strings.TrimSpace(s.String) == "" {
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(s.String), &list); err != nil {
		return []string{unreadable}
	}
	return list
}

func loadEggVariables(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT v.id, e.name, v.env_variable, COALESCE(v.rules, '') FROM egg_variables v JOIN eggs e ON e.id = v.egg_id ORDER BY v.id", nil, func(r *sql.Rows) error {
		var v audit.EggVariable
		if err := r.Scan(&v.ID, &v.Egg, &v.EnvName, &v.Rules); err != nil {
			return err
		}
		in.EggVariables = append(in.EggVariables, v)
		return nil
	})
}

func loadEggImages(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT id, name, COALESCE(docker_images, '{}') FROM eggs ORDER BY id", nil, func(r *sql.Rows) error {
		var e audit.EggImages
		if err := r.Scan(&e.ID, &e.Egg, &e.JSON); err != nil {
			return err
		}
		in.EggImages = append(in.EggImages, e)
		return nil
	})
}

func loadSchedules(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	index := map[uint64]int{}
	err := each(ctx, tx, "SELECT id, name, cron_minute, cron_hour, cron_day_of_month, cron_month, cron_day_of_week FROM schedules ORDER BY id", nil, func(r *sql.Rows) error {
		var s audit.Schedule
		if err := r.Scan(&s.ID, &s.Name, &s.Minute, &s.Hour, &s.DayOfMonth, &s.Month, &s.DayOfWeek); err != nil {
			return err
		}
		index[s.ID] = len(in.Schedules)
		in.Schedules = append(in.Schedules, s)
		return nil
	})
	if err != nil {
		return err
	}
	return each(ctx, tx, "SELECT id, schedule_id, action, payload, time_offset, sequence_id FROM tasks ORDER BY schedule_id, sequence_id, id", nil, func(r *sql.Rows) error {
		var t audit.ScheduleTask
		var scheduleID uint64
		if err := r.Scan(&t.ID, &scheduleID, &t.Action, &t.Payload, &t.TimeOffset, &t.Sequence); err != nil {
			return err
		}
		if i, ok := index[scheduleID]; ok {
			in.Schedules[i].Tasks = append(in.Schedules[i].Tasks, t)
		}
		return nil
	})
}

func loadAPIKeys(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT id, COALESCE(identifier, ''), allowed_ips FROM api_keys ORDER BY id", nil, func(r *sql.Rows) error {
		var k audit.APIKey
		var list sql.NullString
		if err := r.Scan(&k.ID, &k.Identifier, &list); err != nil {
			return err
		}
		k.AllowedIPs = textList(list)
		in.APIKeys = append(in.APIKeys, k)
		return nil
	})
}

func loadSubusers(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT id, server_id, permissions FROM subusers ORDER BY id", nil, func(r *sql.Rows) error {
		var s audit.Subuser
		var list sql.NullString
		if err := r.Scan(&s.ID, &s.ServerID, &list); err != nil {
			return err
		}
		s.Permissions = textList(list)
		in.Subusers = append(in.Subusers, s)
		return nil
	})
}

func loadServers(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT id, external_id, name, description, image, startup, memory, swap, disk, io, cpu, threads, database_limit, allocation_limit, backup_limit FROM servers ORDER BY id", nil, func(r *sql.Rows) error {
		var s audit.Server
		var external, threads sql.NullString
		var databases, allocations sql.NullInt64
		var backups int64
		if err := r.Scan(&s.ID, &external, &s.Name, &s.Description, &s.Image, &s.Startup, &s.Limits.Memory, &s.Limits.Swap, &s.Limits.Disk, &s.Limits.IO, &s.Limits.CPU,
			&threads, &databases, &allocations, &backups); err != nil {
			return err
		}
		if external.Valid {
			s.ExternalID = &external.String
		}
		if threads.Valid {
			s.Limits.Threads = &threads.String
		}
		s.Features = servers.FeatureLimits{Backups: &backups}
		if databases.Valid {
			s.Features.Databases = &databases.Int64
		}
		if allocations.Valid {
			s.Features.Allocations = &allocations.Int64
		}
		in.Servers = append(in.Servers, s)
		return nil
	})
}

func loadNodes(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT id, name, COALESCE(description, ''), location_id, fqdn, scheme, behind_proxy, public, memory, memory_overallocate, disk, disk_overallocate, "+
		"daemonBase, daemonSFTP, daemonListen, upload_size, maintenance_mode FROM nodes ORDER BY id", nil, func(r *sql.Rows) error {
		var n audit.Node
		var s nodes.Settings
		if err := r.Scan(&n.ID, &s.Name, &s.Description, &s.LocationID, &s.FQDN, &s.Scheme, &s.BehindProxy, &s.Public, &s.Memory, &s.MemoryOverallocate, &s.Disk,
			&s.DiskOverallocate, &s.DaemonBase, &s.DaemonSFTP, &s.DaemonListen, &s.UploadSize, &s.MaintenanceMode); err != nil {
			return err
		}
		n.Settings = s
		in.Nodes = append(in.Nodes, n)
		return nil
	})
}

func loadLocations(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT id, short, COALESCE(`long`, '') FROM locations ORDER BY id", nil, func(r *sql.Rows) error {
		var l audit.Location
		if err := r.Scan(&l.ID, &l.Short, &l.Long); err != nil {
			return err
		}
		in.Locations = append(in.Locations, l)
		return nil
	})
}

func loadAllocations(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT id, node_id, ip, port, COALESCE(ip_alias, '') FROM allocations ORDER BY id", nil, func(r *sql.Rows) error {
		var a audit.Allocation
		if err := r.Scan(&a.ID, &a.NodeID, &a.IP, &a.Port, &a.Alias); err != nil {
			return err
		}
		in.Allocations = append(in.Allocations, a)
		return nil
	})
}

// loadUsers also looks in raptor_secrets for the second factor secret, because a user who has set it up with Raptor, or whose old secret was converted and the
// old column emptied, has it only there.
func loadUsers(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT u.id, u.username, u.email, u.password, u.use_totp, "+
		"(COALESCE(u.totp_secret, '') <> '' OR EXISTS (SELECT 1 FROM raptor_secrets s WHERE s.table_name = 'users' AND s.row_id = u.id AND s.column_name = 'totp_secret')) "+
		"FROM users u ORDER BY u.id", nil, func(r *sql.Rows) error {
		var u audit.User
		if err := r.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.UsesTOTP, &u.HasTOTPSecret); err != nil {
			return err
		}
		in.Users = append(in.Users, u)
		return nil
	})
}

func loadRecoveryTokens(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT id, user_id, token FROM recovery_tokens ORDER BY id", nil, func(r *sql.Rows) error {
		var t audit.RecoveryToken
		if err := r.Scan(&t.ID, &t.UserID, &t.Token); err != nil {
			return err
		}
		in.RecoveryTokens = append(in.RecoveryTokens, t)
		return nil
	})
}

// mailPasswordKey is the one setting that holds a secret. It is never read into the audit: its value is an encrypted text that no check can judge, and a
// report that quoted it would carry it to wherever the report is pasted.
const mailPasswordKey = "mail:mailers:smtp:password"

func loadSettings(ctx context.Context, tx *sql.Tx, in *audit.Input) error {
	return each(ctx, tx, "SELECT `key`, `value` FROM settings WHERE `key` LIKE ? ORDER BY id", []any{settings.StoragePrefix + "%"}, func(r *sql.Rows) error {
		var key string
		var value sql.NullString
		if err := r.Scan(&key, &value); err != nil {
			return err
		}
		name := strings.TrimPrefix(key, settings.StoragePrefix)
		if name == mailPasswordKey || !value.Valid {
			return nil
		}
		in.Settings = append(in.Settings, audit.Setting{Key: name, Value: value.String})
		return nil
	})
}
