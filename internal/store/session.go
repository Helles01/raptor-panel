package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Helles01/raptor-panel/internal/auth/session"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
)

// Sessions is the store of the sessions, in the sessions table of the reference (C-003: the payload is Raptor's own JSON, so a session of the
// old panel is not one of this panel).
func (d *DB) Sessions() session.Store { return sessionStore{d} }

type sessionStore struct{ *DB }

func (s sessionStore) Get(ctx context.Context, key string) (session.Record, error) {
	row, err := s.q.GetSession(ctx, key)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return session.Record{}, session.ErrNotFound
	case err != nil:
		return session.Record{}, fmt.Errorf("read session: %w", err)
	}
	return session.Record{
		Key: row.ID, UserID: uint64(row.UserID.Int32), Payload: []byte(row.Payload),
		IP: row.IpAddress.String, UserAgent: row.UserAgent.String, LastActivity: time.Unix(int64(row.LastActivity), 0).UTC(),
	}, nil
}

func (s sessionStore) Put(ctx context.Context, r session.Record) error {
	p := dbq.PutSessionParams{
		ID: r.Key, Payload: string(r.Payload), LastActivity: int32(r.LastActivity.Unix()),
		IpAddress: sql.NullString{String: r.IP, Valid: r.IP != ""}, UserAgent: sql.NullString{String: r.UserAgent, Valid: r.UserAgent != ""},
	}
	if r.UserID != 0 {
		p.UserID = sql.NullInt32{Int32: int32(r.UserID), Valid: true}
	}
	if err := s.q.PutSession(ctx, p); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	return nil
}

func (s sessionStore) Delete(ctx context.Context, key string) error {
	if err := s.q.DeleteSession(ctx, key); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s sessionStore) DeleteForUser(ctx context.Context, userID uint64) error {
	if err := s.q.DeleteSessionsForUser(ctx, sql.NullInt32{Int32: int32(userID), Valid: true}); err != nil {
		return fmt.Errorf("delete the sessions of user %d: %w", userID, err)
	}
	return nil
}

func (s sessionStore) DeleteForUserExcept(ctx context.Context, userID uint64, keep string) error {
	err := s.q.DeleteSessionsForUserExcept(ctx, dbq.DeleteSessionsForUserExceptParams{UserID: sql.NullInt32{Int32: int32(userID), Valid: true}, ID: keep})
	if err != nil {
		return fmt.Errorf("delete the other sessions of user %d: %w", userID, err)
	}
	return nil
}
