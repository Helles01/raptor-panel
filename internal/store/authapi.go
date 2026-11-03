package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/authapi"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/outbox"
)

// AuthapiUsers is the user store of the sign-in endpoints.
func (d *DB) AuthapiUsers() authapi.Users { return authapiUsers{d} }

// AuthapiRecovery is the store of the recovery tokens of a second factor.
func (d *DB) AuthapiRecovery() authapi.RecoveryTokens { return authapiRecovery{d} }

// AuthapiResets is the store of the links for a forgotten password.
func (d *DB) AuthapiResets() authapi.Resets { return authapiResets{d} }

// AuthapiNodes tells the nodes that a user lost access, through the outbox.
func (d *DB) AuthapiNodes() authapi.Nodes { return authapiNodes{d} }

// AuthapiOutbox is the mail of the sign-in endpoints, as jobs of the outbox.
func (d *DB) AuthapiOutbox() authapi.Outbox { return outbox.Mailer{Queue: outboxQueue{d}} }

// AuthapiActivity is the activity log of the sign-in endpoints.
func (d *DB) AuthapiActivity() authapi.ActivityLog { return authapiActivity{d} }

type (
	authapiUsers    struct{ *DB }
	authapiRecovery struct{ *DB }
	authapiResets   struct{ *DB }
	authapiNodes    struct{ *DB }
	authapiActivity struct{ *DB }
)

// userRow is what the three ways of finding a user have in common.
type userRow struct {
	ID                  uint32
	Uuid                string
	Username            string
	Email               string
	Language            string
	RootAdmin           uint8
	UseTotp             uint8
	TotpAuthenticatedAt sql.NullTime
	Password            string
	CreatedAt           sql.NullTime
	UpdatedAt           sql.NullTime
}

func (s authapiUsers) user(ctx context.Context, r userRow, err error) (authapi.User, error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return authapi.User{}, authapi.ErrNoUser
	case err != nil:
		return authapi.User{}, fmt.Errorf("find user: %w", err)
	}
	u := authapi.User{
		ID: uint64(r.ID), UUID: r.Uuid, Username: r.Username, Email: r.Email, Language: r.Language, RootAdmin: r.RootAdmin != 0,
		CreatedAt: timeOf(r.CreatedAt), UpdatedAt: timeOf(r.UpdatedAt), PasswordHash: r.Password, UsesTOTP: r.UseTotp != 0,
		TOTPLastUsed: timePtr(r.TotpAuthenticatedAt),
	}
	if u.UsesTOTP {
		secret, ok, err := s.secret(ctx, "users", u.ID, "totp_secret")
		if err != nil {
			return authapi.User{}, err
		}
		if ok {
			u.TOTPSecret = secret
		}
	}
	return u, nil
}

// ByLogin finds a user by user name or address; the columns compare without regard to case.
func (s authapiUsers) ByLogin(ctx context.Context, field, value string) (authapi.User, error) {
	switch field {
	case "username":
		r, err := s.q.UserByUsername(ctx, value)
		return s.user(ctx, userRow(r), err)
	case "email":
		r, err := s.q.UserByEmail(ctx, value)
		return s.user(ctx, userRow(r), err)
	}
	return authapi.User{}, fmt.Errorf("store: a user is found by username or email, not by %q", field)
}

func (s authapiUsers) ByID(ctx context.Context, id uint64) (authapi.User, error) {
	if id == 0 || id > 1<<32-1 {
		return authapi.User{}, authapi.ErrNoUser
	}
	r, err := s.q.UserByID(ctx, uint32(id))
	return s.user(ctx, userRow(r), err)
}

func (s authapiUsers) ByEmail(ctx context.Context, email string) (authapi.User, error) {
	r, err := s.q.UserByEmail(ctx, email)
	return s.user(ctx, userRow(r), err)
}

// UseTOTPStep is one conditional statement: it changes the row only if the step is later than the one stored, so of two requests with the same
// code only one gets true.
func (s authapiUsers) UseTOTPStep(ctx context.Context, userID uint64, step time.Time) (bool, error) {
	t := sql.NullTime{Time: step.UTC(), Valid: true}
	n, err := s.q.UseTOTPStep(ctx, dbq.UseTOTPStepParams{TotpAuthenticatedAt: t, ID: uint32(userID), TotpAuthenticatedAt_2: t})
	if err != nil {
		return false, fmt.Errorf("store the used code: %w", err)
	}
	return n == 1, nil
}

func (s authapiUsers) SetPassword(ctx context.Context, userID uint64, hash string) error {
	n, err := s.q.SetUserPassword(ctx, dbq.SetUserPasswordParams{Password: hash, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(userID)})
	switch {
	case err != nil:
		return fmt.Errorf("store the password: %w", err)
	case n == 0:
		return authapi.ErrNoUser
	}
	return nil
}

func (s authapiRecovery) ForUser(ctx context.Context, userID uint64) ([]authapi.RecoveryToken, error) {
	rows, err := s.q.RecoveryTokensForUser(ctx, uint32(userID))
	if err != nil {
		return nil, fmt.Errorf("read the recovery tokens of user %d: %w", userID, err)
	}
	out := make([]authapi.RecoveryToken, len(rows))
	for i, r := range rows {
		out[i] = authapi.RecoveryToken{ID: r.ID, Hash: r.Token}
	}
	return out, nil
}

// Delete spends a token: true for the one request whose delete removed the row.
func (s authapiRecovery) Delete(ctx context.Context, id uint64) (bool, error) {
	n, err := s.q.DeleteRecoveryToken(ctx, id)
	if err != nil {
		return false, fmt.Errorf("spend the recovery token: %w", err)
	}
	return n == 1, nil
}

func (s authapiResets) Latest(ctx context.Context, email string) (passwordreset.Record, bool, error) {
	row, err := s.q.LatestPasswordReset(ctx, email)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return passwordreset.Record{}, false, nil
	case err != nil:
		return passwordreset.Record{}, false, fmt.Errorf("read the reset link: %w", err)
	}
	return passwordreset.Record{Hash: row.Token, CreatedAt: row.CreatedAt.UTC()}, true, nil
}

// Replace makes the record the only one of the address: an older link stops working.
func (s authapiResets) Replace(ctx context.Context, email string, rec passwordreset.Record) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.DeletePasswordResets(ctx, email); err != nil {
			return fmt.Errorf("remove the old reset link: %w", err)
		}
		if err := q.InsertPasswordReset(ctx, dbq.InsertPasswordResetParams{Email: email, Token: rec.Hash, CreatedAt: rec.CreatedAt.UTC()}); err != nil {
			return fmt.Errorf("store the reset link: %w", err)
		}
		return nil
	})
}

// Consume spends the link: true for the one request whose delete removed it.
func (s authapiResets) Consume(ctx context.Context, email string) (bool, error) {
	n, err := s.q.DeletePasswordResets(ctx, email)
	if err != nil {
		return false, fmt.Errorf("spend the reset link: %w", err)
	}
	return n > 0, nil
}

// DeauthorizeUser queues, for every node where the user owns or is allowed on a server, the order to take their access away. The orders are
// stored in one transaction, so a crash leaves all of them or none.
func (s authapiNodes) DeauthorizeUser(ctx context.Context, userID uint64) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		uuid, err := q.UserUUID(ctx, uint32(userID))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil // no user, nothing to take away
		case err != nil:
			return fmt.Errorf("find user %d: %w", userID, err)
		}
		nodes, err := q.NodesOfUser(ctx, dbq.NodesOfUserParams{OwnerID: uint32(userID), UserID: uint32(userID)})
		if err != nil {
			return fmt.Errorf("find the nodes of user %d: %w", userID, err)
		}
		for _, node := range nodes {
			job, err := outbox.DeauthorizeJob(outbox.Deauthorization{NodeID: uint64(node), UserUUID: uuid})
			if err != nil {
				return err
			}
			if err := q.EnqueueJob(ctx, dbq.EnqueueJobParams{Kind: job.Kind, Payload: string(job.Payload), RunAt: s.now().UTC()}); err != nil {
				return fmt.Errorf("queue the order for node %d: %w", node, err)
			}
		}
		return nil
	})
}

func (s authapiActivity) Log(ctx context.Context, e authapi.LogEntry) error {
	return s.logActivity(ctx, entry{Event: e.Event, Subject: e.Subject, Actor: e.Actor, IP: ipOf(e.Properties), Properties: e.Properties})
}
