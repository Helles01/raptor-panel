// Package store is the SQL layer: it implements the storage interfaces of the endpoint packages on MariaDB/MySQL, over the queries of
// internal/db/queries (checked against the baseline schema by sqlc). Each interface gets a small adapter of its own, because the same
// method name means different things in different packages (ByID of the authorizer is not ByID of the sign-in); a constructor of DB gives
// the adapter as the interface it implements.
//
// Secrets (the secret of an API key, the token of a node, the secret of a second factor) are never read from the old Laravel columns: they
// are opened from raptor_secrets with the Box (C-002).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

// userModel is how the reference writes the model of a user in the activity log (its morph map), so that old rows and new ones agree.
const userModel = "Pterodactyl\\Models\\User"

// DB is the SQL layer over an open database.
type DB struct {
	sql *sql.DB
	q   *dbq.Queries
	box *secrets.Box
	// now is the clock; time.Now in the program.
	now func() time.Time
}

// New returns the SQL layer. box opens and seals the secrets in raptor_secrets.
func New(sqlDB *sql.DB, box *secrets.Box) (*DB, error) {
	if sqlDB == nil || box == nil {
		return nil, errors.New("store: a database and a secrets box are required")
	}
	return &DB{sql: sqlDB, q: dbq.New(sqlDB), box: box, now: time.Now}, nil
}

// secret opens a value from raptor_secrets. ok is false when there is none.
func (d *DB) secret(ctx context.Context, table string, id uint64, column string) (value string, ok bool, err error) {
	sealed, err := d.q.GetSecret(ctx, dbq.GetSecretParams{TableName: table, RowID: id, ColumnName: column})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("read secret %s.%s of row %d: %w", table, column, id, err)
	}
	value, err = d.box.Open(sealed, secrets.Context(table, id, column))
	if err != nil {
		// The text of the error says nothing of the value; the row is named so that it can be found.
		return "", false, fmt.Errorf("open secret %s.%s of row %d: %w", table, column, id, err)
	}
	return value, true, nil
}

// inTx runs fn in a transaction with the queries bound to it.
func (d *DB) inTx(ctx context.Context, fn func(q *dbq.Queries) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if err := fn(d.q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func nullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.UTC(), Valid: true}
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func timeOf(t sql.NullTime) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

// entry is a line of the activity log about a user.
type entry struct {
	Event      string
	Subject    uint64
	Actor      uint64
	APIKeyID   uint64
	IP         string
	Properties string
}

// logActivity writes an entry and the subject it is about, in one transaction.
func (d *DB) logActivity(ctx context.Context, e entry) error {
	return d.inTx(ctx, func(q *dbq.Queries) error {
		params := dbq.InsertActivityLogParams{Event: e.Event, Ip: e.IP, Properties: e.Properties, Timestamp: d.now().UTC()}
		if e.Actor != 0 {
			params.ActorType = sql.NullString{String: userModel, Valid: true}
			params.ActorID = sql.NullInt64{Int64: int64(e.Actor), Valid: true}
		}
		if e.APIKeyID != 0 {
			params.ApiKeyID = sql.NullInt32{Int32: int32(e.APIKeyID), Valid: true}
		}
		id, err := q.InsertActivityLog(ctx, params)
		if err != nil {
			return fmt.Errorf("write the activity log: %w", err)
		}
		if e.Subject == 0 {
			return nil
		}
		if err := q.InsertActivitySubject(ctx, dbq.InsertActivitySubjectParams{ActivityLogID: uint64(id), SubjectType: userModel, SubjectID: e.Subject}); err != nil {
			return fmt.Errorf("write the subject of the activity log: %w", err)
		}
		return nil
	})
}

// ipOf reads the address out of the properties of an entry, where the endpoints put it.
func ipOf(properties string) string {
	var p struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal([]byte(properties), &p) != nil {
		return ""
	}
	return p.IP
}
