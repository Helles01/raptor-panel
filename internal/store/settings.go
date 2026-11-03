package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/api/settingsapi"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
)

// SettingsStore is the store of what a server's owner changes about it.
func (d *DB) SettingsStore() settingsapi.Store { return settingsStore{d} }

type settingsStore struct{ *DB }

// orderedImages reads the object of the images of an egg in the order of the file: a map would lose it, and the first image is the egg's default.
func orderedImages(raw string) settingsapi.Images {
	dec := json.NewDecoder(strings.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var out settingsapi.Images
	for dec.More() {
		label, err := dec.Token()
		if err != nil {
			return out
		}
		var image string
		if err := dec.Decode(&image); err != nil {
			return out
		}
		if l, ok := label.(string); ok {
			out = append(out, settingsapi.Image{Label: l, Image: image})
		}
	}
	return out
}

func (s settingsStore) Server(ctx context.Context, id uint64) (settingsapi.Server, error) {
	srv, err := clientServers{s.DB}.One(ctx, id)
	if errors.Is(err, clientapi.ErrNotFound) {
		return settingsapi.Server{}, settingsapi.ErrNotFound
	} else if err != nil {
		return settingsapi.Server{}, err
	}
	images, err := s.q.EggImagesOfServer(ctx, uint32(id))
	if err != nil {
		return settingsapi.Server{}, fmt.Errorf("read the images of the egg of server %d: %w", id, err)
	}
	return settingsapi.Server{Server: srv, EggImages: orderedImages(images.String)}, nil
}

// SetVariable stores the value of one of the egg's variables for the server. The server's row is locked while that is done, so two requests for a
// variable that has no value yet cannot both make one.
func (s settingsStore) SetVariable(ctx context.Context, serverID uint64, envName, value string) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockServer(ctx, uint32(serverID)); errors.Is(err, sql.ErrNoRows) {
			return settingsapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock server %d: %w", serverID, err)
		}
		variable, err := q.VariableOfServer(ctx, dbq.VariableOfServerParams{ID: uint32(serverID), EnvVariable: envName})
		if errors.Is(err, sql.ErrNoRows) {
			return settingsapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("find the variable %s: %w", envName, err)
		}
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		owner := sql.NullInt32{Int32: int32(serverID), Valid: true}
		// An update that finds the value the same changes no row, so the rows are counted to tell "none yet" from "no change".
		rows, err := q.CountServerVariableRows(ctx, dbq.CountServerVariableRowsParams{ServerID: owner, VariableID: variable})
		if err != nil {
			return fmt.Errorf("look for the value of the variable %s: %w", envName, err)
		}
		if rows == 0 {
			if err := q.InsertServerVariable(ctx, dbq.InsertServerVariableParams{ServerID: owner, VariableID: variable, VariableValue: value, CreatedAt: now, UpdatedAt: now}); err != nil {
				return fmt.Errorf("store the variable %s: %w", envName, err)
			}
			return nil
		}
		if _, err := q.UpdateServerVariable(ctx, dbq.UpdateServerVariableParams{VariableValue: value, UpdatedAt: now, ServerID: owner, VariableID: variable}); err != nil {
			return fmt.Errorf("store the variable %s: %w", envName, err)
		}
		return nil
	})
}

// lockedServer runs fn with the server's row locked, or answers ErrNotFound for a server that is gone.
func (s settingsStore) lockedServer(ctx context.Context, serverID uint64, fn func(q *dbq.Queries, at sql.NullTime) error) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockServer(ctx, uint32(serverID)); errors.Is(err, sql.ErrNoRows) {
			return settingsapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock server %d: %w", serverID, err)
		}
		return fn(q, sql.NullTime{Time: s.now().UTC(), Valid: true})
	})
}

func (s settingsStore) SetImage(ctx context.Context, serverID uint64, image string) error {
	return s.lockedServer(ctx, serverID, func(q *dbq.Queries, at sql.NullTime) error {
		if _, err := q.SetServerImage(ctx, dbq.SetServerImageParams{Image: image, UpdatedAt: at, ID: uint32(serverID)}); err != nil {
			return fmt.Errorf("store the image: %w", err)
		}
		return nil
	})
}

func (s settingsStore) Rename(ctx context.Context, serverID uint64, name, description string) error {
	return s.lockedServer(ctx, serverID, func(q *dbq.Queries, at sql.NullTime) error {
		if _, err := q.RenameServer(ctx, dbq.RenameServerParams{Name: name, Description: description, UpdatedAt: at, ID: uint32(serverID)}); err != nil {
			return fmt.Errorf("rename the server: %w", err)
		}
		return nil
	})
}

// SetStatus changes the status in one statement that holds only while the server still has the status the change was made against.
func (s settingsStore) SetStatus(ctx context.Context, serverID uint64, from, to string) (bool, error) {
	n, err := s.q.SetInstallStatus(ctx, dbq.SetInstallStatusParams{
		Status: sql.NullString{String: to, Valid: to != ""}, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(serverID),
		FromStatus: sql.NullString{String: from, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("change the status of server %d: %w", serverID, err)
	}
	return n == 1, nil
}
