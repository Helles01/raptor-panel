package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/api/appallocs"
	"github.com/Helles01/raptor-panel/internal/api/appkit"
	"github.com/Helles01/raptor-panel/internal/api/applocations"
	"github.com/Helles01/raptor-panel/internal/api/appnests"
	"github.com/Helles01/raptor-panel/internal/api/appusers"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/outbox"
)

// AppUsers, AppLocations, AppAllocations and AppNests are the stores of the Application API for users, locations, allocations, nests and eggs.
func (d *DB) AppUsers() appusers.Store               { return appUsers{d} }
func (d *DB) AppLocations() applocations.Store       { return appLocations{d} }
func (d *DB) AppAllocations() appallocs.Store        { return appAllocations{d} }
func (d *DB) AppNests() appnests.Store               { return appNests{d} }
func (d *DB) AppUsersOutbox() appusers.Outbox        { return outbox.Mailer{Queue: outboxQueue{d}} }
func (d *DB) AppUsersActivity() appusers.ActivityLog { return appUsersActivity{d} }

type (
	appUsers         struct{ *DB }
	appLocations     struct{ *DB }
	appAllocations   struct{ *DB }
	appNests         struct{ *DB }
	appUsersActivity struct{ *DB }
)

// --- users

func (s appUsers) withRelations(ctx context.Context, u *appkit.User, load appkit.Includes) error {
	if load["servers"] {
		var err error
		if u.Servers, err = s.serversOf(ctx, "`servers`.`owner_id` = ?", u.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s appUsers) List(ctx context.Context, q listquery.Query, page, perPage int, load appkit.Includes) ([]appkit.User, int, error) {
	where, args := q.Where()
	list, total, err := pageOf(ctx, s.DB, scanUser, "users", userSelect, where, q.OrderBy("id"), args, page, perPage)
	if err != nil {
		return nil, 0, err
	}
	for i := range list {
		if err := s.withRelations(ctx, &list[i], load); err != nil {
			return nil, 0, err
		}
	}
	return list, total, nil
}

func (s appUsers) find(ctx context.Context, where string, arg any, load appkit.Includes) (appkit.User, error) {
	u, err := one(ctx, s.DB, scanUser, userSelect+" WHERE "+where, arg)
	if err != nil {
		return u, err
	}
	return u, s.withRelations(ctx, &u, load)
}

func (s appUsers) Find(ctx context.Context, id uint64, load appkit.Includes) (appkit.User, error) {
	return s.find(ctx, "`users`.`id` = ?", id, load)
}

func (s appUsers) FindExternal(ctx context.Context, externalID string, load appkit.Includes) (appkit.User, error) {
	return s.find(ctx, "`users`.`external_id` = ?", externalID, load)
}

// Create stores the account and, if the owner is to choose a password, the setup token for the address, in one transaction.
func (s appUsers) create(ctx context.Context, r appusers.CreateRequest) (appkit.User, error) {
	var id uint64
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		u := r.User
		if err := s.checkExternalID(ctx, q, u.ExternalID, 0); err != nil {
			return err
		}
		p := dbq.InsertUserParams{
			Uuid: u.UUID, Username: u.Username, Email: u.Email, Password: u.PasswordHash, Language: u.Language, RootAdmin: boolByte(u.Admin),
			NameFirst: sql.NullString{String: u.FirstName, Valid: true}, NameLast: sql.NullString{String: u.LastName, Valid: true}, CreatedAt: now, UpdatedAt: now,
		}
		if u.ExternalID != nil {
			p.ExternalID = sql.NullString{String: *u.ExternalID, Valid: true}
		}
		res, err := q.InsertUser(ctx, p)
		if field, taken := duplicate(err); taken {
			return &appkit.TakenError{Field: field}
		}
		if err != nil {
			return fmt.Errorf("store the user: %w", err)
		}
		id = uint64(res)
		if r.SetupTokenHash != "" {
			if _, err := q.DeletePasswordResets(ctx, u.Email); err != nil {
				return fmt.Errorf("remove old reset links: %w", err)
			}
			if err := q.InsertPasswordReset(ctx, dbq.InsertPasswordResetParams{Email: u.Email, Token: r.SetupTokenHash, CreatedAt: now.Time}); err != nil {
				return fmt.Errorf("store the setup token: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return appkit.User{}, err
	}
	return s.Find(ctx, id, nil)
}
