package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/subuserapi"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/users"
)

// ClientSubusers is the subuser store of the client API.
func (d *DB) ClientSubusers() subuserapi.Store { return clientSubusers{d} }

// SubuserOutbox is the mail of the subuser routes, as jobs of the outbox.
func (d *DB) SubuserOutbox() subuserapi.Outbox { return outbox.Mailer{Queue: outboxQueue{d}} }

type clientSubusers struct{ *DB }

const subuserSelect = "SELECT `subusers`.`user_id`, `subusers`.`permissions`, `users`.`uuid`, `users`.`username`, `users`.`email`, `users`.`use_totp`, `users`.`created_at` " +
	"FROM `subusers` JOIN `users` ON `users`.`id` = `subusers`.`user_id`"

func scanSubuser(r scanner) (subuserapi.Subuser, error) {
	var (
		s       subuserapi.Subuser
		perms   sql.NullString
		totp    bool
		created sql.NullTime
	)
	if err := r.Scan(&s.UserID, &perms, &s.User.UUID, &s.User.Username, &s.User.Email, &totp, &created); err != nil {
		return s, err
	}
	s.User.UsesTOTP, s.User.CreatedAt, s.Permissions = totp, at(created), jsonStrings(perms)
	return s, nil
}

func (s clientSubusers) List(ctx context.Context, serverID uint64) ([]subuserapi.Subuser, error) {
	list, err := all(ctx, s.DB, scanSubuser, subuserSelect+" WHERE `subusers`.`server_id` = ? ORDER BY `subusers`.`id`", serverID)
	if err != nil {
		return nil, fmt.Errorf("read the subusers of server %d: %w", serverID, err)
	}
	return list, nil
}

func (s clientSubusers) Find(ctx context.Context, serverID uint64, userUUID string) (subuserapi.Subuser, error) {
	sub, err := one(ctx, s.DB, scanSubuser, subuserSelect+" WHERE `subusers`.`server_id` = ? AND `users`.`uuid` = ? ORDER BY `subusers`.`id` LIMIT 1", serverID, userUUID)
	return sub, notFound(err)
}

// notFound turns the store's not-found into the one of the subuser routes.
func notFound(err error) error {
	if errors.Is(err, appkit.ErrNotFound) {
		return subuserapi.ErrNotFound
	}
	return err
}

func (s clientSubusers) UserByEmail(ctx context.Context, email string) (subuserapi.UserRef, error) {
	row, err := s.q.UserRefByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) {
		return subuserapi.UserRef{}, subuserapi.ErrNotFound
	} else if err != nil {
		return subuserapi.UserRef{}, fmt.Errorf("find the user with address %q: %w", email, err)
	}
	return subuserapi.UserRef{ID: uint64(row.ID), UUID: row.Uuid, Username: row.Username}, nil
}

func (s clientSubusers) IsSubuser(ctx context.Context, serverID, userID uint64) (bool, error) {
	if !fits(serverID, userID) {
		return false, nil
	}
	n, err := s.q.CountSubuserEntries(ctx, dbq.CountSubuserEntriesParams{ServerID: uint32(serverID), UserID: uint32(userID)})
	if err != nil {
		return false, fmt.Errorf("count the subuser entries: %w", err)
	}
	return n > 0, nil
}

func (s clientSubusers) ServerName(ctx context.Context, serverID uint64) (string, error) {
	if !fits(serverID) {
		return "", subuserapi.ErrNotFound
	}
	name, err := s.q.ServerNameOf(ctx, uint32(serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", subuserapi.ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("read server %d: %w", serverID, err)
	}
	return name, nil
}

func permissionsJSON(p []string) (sql.NullString, error) {
	if p == nil {
		p = []string{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode the permissions: %w", err)
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

// Add stores the subuser, and the account if the address had none, in one transaction with the server's row locked: two requests to add the same person
// cannot both find that they are not a subuser yet (the table has no unique key for it).
func (s clientSubusers) Add(ctx context.Context, r subuserapi.AddRequest) (subuserapi.Subuser, error) {
	if !fits(r.ServerID, r.UserID) {
		return subuserapi.Subuser{}, subuserapi.ErrNotFound
	}
	perms, err := permissionsJSON(r.Permissions)
	if err != nil {
		return subuserapi.Subuser{}, err
	}
	userID := r.UserID
	err = appUsers{s.DB}.retryDeadlock(func() error {
		return s.inTx(ctx, func(q *dbq.Queries) error {
			if _, err := q.LockServer(ctx, uint32(r.ServerID)); errors.Is(err, sql.ErrNoRows) {
				return subuserapi.ErrNotFound
			} else if err != nil {
				return fmt.Errorf("lock server %d: %w", r.ServerID, err)
			}
			now := sql.NullTime{Time: s.now().UTC(), Valid: true}
			if r.NewUser != nil {
				id, err := s.insertUser(ctx, q, *r.NewUser, r.SetupTokenHash, now)
				if err != nil {
					return err
				}
				userID = id
			}
			n, err := q.CountSubuserEntries(ctx, dbq.CountSubuserEntriesParams{ServerID: uint32(r.ServerID), UserID: uint32(userID)})
			if err != nil {
				return fmt.Errorf("count the subuser entries: %w", err)
			}
			if n > 0 {
				return subuserapi.ErrExists
			}
			return q.InsertSubuser(ctx, dbq.InsertSubuserParams{UserID: uint32(userID), ServerID: uint32(r.ServerID), Permissions: perms, CreatedAt: now, UpdatedAt: now})
		})
	})
	if err != nil {
		return subuserapi.Subuser{}, err
	}
	sub, err := one(ctx, s.DB, scanSubuser, subuserSelect+" WHERE `subusers`.`server_id` = ? AND `subusers`.`user_id` = ? ORDER BY `subusers`.`id` LIMIT 1", r.ServerID, userID)
	return sub, notFound(err)
}

// insertUser makes the account of a person who is invited by the address, with the link they set a password by.
func (s clientSubusers) insertUser(ctx context.Context, q *dbq.Queries, u users.Record, setupHash string, now sql.NullTime) (uint64, error) {
	p := dbq.InsertUserParams{
		Uuid: u.UUID, Username: u.Username, Email: u.Email, Password: u.PasswordHash, Language: u.Language, RootAdmin: boolByte(u.Admin),
		NameFirst: sql.NullString{String: u.FirstName, Valid: true}, NameLast: sql.NullString{String: u.LastName, Valid: true}, CreatedAt: now, UpdatedAt: now,
	}
	res, err := q.InsertUser(ctx, p)
	if field, taken := duplicate(err); taken {
		switch field {
		case "username":
			return 0, subuserapi.ErrUsernameTaken
		case "email":
			return 0, subuserapi.ErrEmailTaken
		}
		return 0, fmt.Errorf("store the account: %w", err)
	}
	if err != nil {
		return 0, fmt.Errorf("store the account: %w", err)
	}
	if setupHash != "" {
		if _, err := q.DeletePasswordResets(ctx, u.Email); err != nil {
			return 0, fmt.Errorf("remove old reset links: %w", err)
		}
		if err := q.InsertPasswordReset(ctx, dbq.InsertPasswordResetParams{Email: u.Email, Token: setupHash, CreatedAt: now.Time}); err != nil {
			return 0, fmt.Errorf("store the setup token: %w", err)
		}
	}
	return uint64(res), nil
}

// change runs a change of a subuser's entry with the server's row and the entry locked; with revoke, the order to take the person's access on the node
// away is queued in the same transaction, so that the change takes effect at once and never without the order.
func (s clientSubusers) change(ctx context.Context, serverID, userID uint64, revoke bool, apply func(q *dbq.Queries) error) error {
	if !fits(serverID, userID) {
		return subuserapi.ErrNotFound
	}
	return appUsers{s.DB}.retryDeadlock(func() error {
		return s.inTx(ctx, func(q *dbq.Queries) error {
			if _, err := q.LockServer(ctx, uint32(serverID)); errors.Is(err, sql.ErrNoRows) {
				return subuserapi.ErrNotFound
			} else if err != nil {
				return fmt.Errorf("lock server %d: %w", serverID, err)
			}
			entries, err := q.LockSubuserEntries(ctx, dbq.LockSubuserEntriesParams{ServerID: uint32(serverID), UserID: uint32(userID)})
			if err != nil {
				return fmt.Errorf("lock the subuser entries: %w", err)
			}
			if len(entries) == 0 {
				return subuserapi.ErrNotFound
			}
			if err := apply(q); err != nil {
				return err
			}
			if !revoke {
				return nil
			}
			srv, err := q.ServerNodeAndUUID(ctx, uint32(serverID))
			if err != nil {
				return fmt.Errorf("read server %d: %w", serverID, err)
			}
			uuid, err := q.UserUUID(ctx, uint32(userID))
			if err != nil {
				return fmt.Errorf("find user %d: %w", userID, err)
			}
			job, err := outbox.DeauthorizeJob(outbox.Deauthorization{NodeID: uint64(srv.NodeID), UserUUID: uuid, ServerUUIDs: []string{srv.Uuid}})
			if err != nil {
				return err
			}
			return q.EnqueueJob(ctx, dbq.EnqueueJobParams{Kind: job.Kind, Payload: string(job.Payload), RunAt: s.now().UTC()})
		})
	})
}

func (s clientSubusers) SetPermissions(ctx context.Context, serverID, userID uint64, permissions []string, revoke bool) error {
	perms, err := permissionsJSON(permissions)
	if err != nil {
		return err
	}
	return s.change(ctx, serverID, userID, revoke, func(q *dbq.Queries) error {
		return q.UpdateSubuserPermissions(ctx, dbq.UpdateSubuserPermissionsParams{Permissions: perms, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ServerID: uint32(serverID), UserID: uint32(userID)})
	})
}

func (s clientSubusers) Remove(ctx context.Context, serverID, userID uint64, revoke bool) error {
	return s.change(ctx, serverID, userID, revoke, func(q *dbq.Queries) error {
		return q.DeleteSubuserEntries(ctx, dbq.DeleteSubuserEntriesParams{ServerID: uint32(serverID), UserID: uint32(userID)})
	})
}
