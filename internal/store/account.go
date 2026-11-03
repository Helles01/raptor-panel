package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/accountapi"
	"github.com/Helles01/raptor-panel/internal/db/dbq"
)

// apiKeyModel, sshKeyModel are how the reference writes the models of the two kinds of keys in the activity log.
const (
	apiKeyModel = "Pterodactyl\\Models\\ApiKey"
	sshKeyModel = "Pterodactyl\\Models\\UserSSHKey"
)

// AccountStore is the store of a user's own account.
func (d *DB) AccountStore() accountapi.Store { return accountStore{d} }

// AccountNodes tells the nodes that a user lost access, through the outbox.
func (d *DB) AccountNodes() accountapi.Nodes { return authapiNodes{d} }

// AccountActivity is the activity log of the account routes.
func (d *DB) AccountActivity() accountapi.ActivityLog { return accountActivity{d} }

type (
	accountStore    struct{ *DB }
	accountActivity struct{ *DB }
)

func (s accountStore) Account(ctx context.Context, userID uint64) (accountapi.Account, error) {
	if userID == 0 || userID > 1<<32-1 {
		return accountapi.Account{}, accountapi.ErrNotFound
	}
	row, err := s.q.AccountForSettings(ctx, uint32(userID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return accountapi.Account{}, accountapi.ErrNotFound
	case err != nil:
		return accountapi.Account{}, fmt.Errorf("find user %d: %w", userID, err)
	}
	a := accountapi.Account{ID: uint64(row.ID), UUID: row.Uuid, Email: row.Email, PasswordHash: row.Password, UsesTOTP: row.UseTotp != 0}
	secret, ok, err := s.secret(ctx, "users", userID, "totp_secret")
	if err != nil {
		return accountapi.Account{}, err
	}
	if ok {
		a.TOTPSecret = secret
	}
	return a, nil
}

// SetEmail stores the new address and deletes the reset links of the old one in the same transaction.
func (s accountStore) SetEmail(ctx context.Context, userID uint64, oldEmail, newEmail string) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		n, err := q.SetUserEmail(ctx, dbq.SetUserEmailParams{Email: newEmail, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(userID)})
		if _, taken := duplicate(err); taken {
			return accountapi.ErrEmailTaken
		}
		if err != nil {
			return fmt.Errorf("store the address: %w", err)
		}
		if n == 0 {
			return accountapi.ErrNotFound
		}
		if _, err := q.DeletePasswordResets(ctx, oldEmail); err != nil {
			return fmt.Errorf("remove the reset links of the old address: %w", err)
		}
		return nil
	})
}

// SetPassword stores the hash and deletes the reset links of the account's address in the same transaction.
func (s accountStore) SetPassword(ctx context.Context, userID uint64, hash string) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		email, err := q.UserEmail(ctx, uint32(userID))
		if errors.Is(err, sql.ErrNoRows) {
			return accountapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("find user %d: %w", userID, err)
		}
		if _, err := q.SetUserPassword(ctx, dbq.SetUserPasswordParams{Password: hash, UpdatedAt: sql.NullTime{Time: s.now().UTC(), Valid: true}, ID: uint32(userID)}); err != nil {
			return fmt.Errorf("store the password: %w", err)
		}
		if _, err := q.DeletePasswordResets(ctx, email); err != nil {
			return fmt.Errorf("remove the reset links: %w", err)
		}
		return nil
	})
}

// SetTOTPSecret stores a new secret, sealed; the code of the one before is forgotten with it.
func (s accountStore) SetTOTPSecret(ctx context.Context, userID uint64, secret string) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.UserEmail(ctx, uint32(userID)); errors.Is(err, sql.ErrNoRows) {
			return accountapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("find user %d: %w", userID, err)
		}
		if err := s.seal(ctx, q, "users", userID, "totp_secret", secret); err != nil {
			return err
		}
		if err := q.ResetTotpAuthentication(ctx, uint32(userID)); err != nil {
			return fmt.Errorf("forget the last code: %w", err)
		}
		return nil
	})
}

// EnableTOTP replaces the recovery tokens and switches two-factor authentication on, in one transaction.
func (s accountStore) EnableTOTP(ctx context.Context, userID uint64, hashes []string, usedAt time.Time) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		if err := q.DeleteRecoveryTokensOfUser(ctx, uint32(userID)); err != nil {
			return fmt.Errorf("remove the old recovery tokens: %w", err)
		}
		for _, h := range hashes {
			if err := q.InsertRecoveryToken(ctx, dbq.InsertRecoveryTokenParams{UserID: uint32(userID), Token: h, CreatedAt: now}); err != nil {
				return fmt.Errorf("store a recovery token: %w", err)
			}
		}
		n, err := q.EnableTwoFactor(ctx, dbq.EnableTwoFactorParams{TotpAuthenticatedAt: sql.NullTime{Time: usedAt.UTC(), Valid: true}, UpdatedAt: now, ID: uint32(userID)})
		switch {
		case err != nil:
			return fmt.Errorf("switch two-factor authentication on: %w", err)
		case n == 0:
			return accountapi.ErrNotFound
		}
		return nil
	})
}

// DisableTOTP switches it off and removes the secret and the recovery tokens.
func (s accountStore) DisableTOTP(ctx context.Context, userID uint64) error {
	return s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.ClearTwoFactor(ctx, uint32(userID)); err != nil {
			return fmt.Errorf("switch two-factor authentication off: %w", err)
		}
		if err := q.DeleteRecoveryTokensOfUser(ctx, uint32(userID)); err != nil {
			return fmt.Errorf("remove the recovery tokens: %w", err)
		}
		if err := q.DeleteSecret(ctx, dbq.DeleteSecretParams{TableName: "users", RowID: userID, ColumnName: "totp_secret"}); err != nil {
			return fmt.Errorf("remove the secret: %w", err)
		}
		return nil
	})
}

func keyRecord(id uint32, identifier, memo, allowed sql.NullString, last, created sql.NullTime) accountapi.KeyRecord {
	return accountapi.KeyRecord{ID: uint64(id), Identifier: identifier.String, Description: memo.String, AllowedIPs: allowedIPs(allowed), LastUsedAt: timePtr(last), CreatedAt: timeOf(created)}
}

func (s accountStore) APIKeys(ctx context.Context, userID uint64) ([]accountapi.KeyRecord, error) {
	rows, err := s.q.AccountAPIKeys(ctx, uint32(userID))
	if err != nil {
		return nil, fmt.Errorf("read the keys of user %d: %w", userID, err)
	}
	out := make([]accountapi.KeyRecord, len(rows))
	for i, r := range rows {
		out[i] = keyRecord(r.ID, r.Identifier, r.Memo, r.AllowedIps, r.LastUsedAt, r.CreatedAt)
	}
	return out, nil
}

// CreateKey counts the account keys with the user's row locked, so that two requests cannot both pass the limit.
func (s accountStore) CreateKey(ctx context.Context, userID uint64, k accountapi.NewKey, check func(count int) error) (accountapi.KeyRecord, error) {
	var rec accountapi.KeyRecord
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockUser(ctx, uint32(userID)); errors.Is(err, sql.ErrNoRows) {
			return accountapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock user %d: %w", userID, err)
		}
		count, err := q.CountAccountKeysOfUser(ctx, uint32(userID))
		if err != nil {
			return fmt.Errorf("count the keys of user %d: %w", userID, err)
		}
		if err := check(int(count)); err != nil {
			return err
		}
		now := s.now().UTC()
		p := dbq.InsertAPIKeyParams{
			UserID: uint32(userID), KeyType: 1, Identifier: sql.NullString{String: k.Identifier, Valid: true}, Memo: sql.NullString{String: k.Description, Valid: true},
			CreatedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: sql.NullTime{Time: now, Valid: true},
		}
		if len(k.AllowedIPs) > 0 {
			list, err := json.Marshal(k.AllowedIPs)
			if err != nil {
				return fmt.Errorf("write the allowed addresses: %w", err)
			}
			p.AllowedIps = sql.NullString{String: string(list), Valid: true}
		}
		id, err := q.InsertAPIKey(ctx, p)
		if _, taken := duplicate(err); taken {
			return fmt.Errorf("the identifier of the key is taken: %w", err)
		}
		if err != nil {
			return fmt.Errorf("store the key: %w", err)
		}
		if err := s.seal(ctx, q, "api_keys", uint64(id), "token_sha256", k.SecretHash); err != nil {
			return err
		}
		rec = accountapi.KeyRecord{ID: uint64(id), Identifier: k.Identifier, Description: k.Description, AllowedIPs: k.AllowedIPs, CreatedAt: now}
		return nil
	})
	return rec, err
}

func (s accountStore) DeleteKey(ctx context.Context, userID uint64, identifier string) (accountapi.KeyRecord, error) {
	var rec accountapi.KeyRecord
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		r, err := q.LockAccountKey(ctx, dbq.LockAccountKeyParams{UserID: uint32(userID), Identifier: sql.NullString{String: identifier, Valid: true}})
		if errors.Is(err, sql.ErrNoRows) {
			return accountapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("find the key: %w", err)
		}
		rec = keyRecord(r.ID, r.Identifier, r.Memo, r.AllowedIps, r.LastUsedAt, r.CreatedAt)
		if err := q.DeleteSecretsOfRow(ctx, dbq.DeleteSecretsOfRowParams{TableName: "api_keys", RowID: uint64(r.ID)}); err != nil {
			return fmt.Errorf("remove the secrets of the key: %w", err)
		}
		return q.DeleteAPIKey(ctx, r.ID)
	})
	return rec, err
}

func sshRecord(id uint32, name, fingerprint, public string, created sql.NullTime) accountapi.SSHKeyRecord {
	return accountapi.SSHKeyRecord{ID: uint64(id), Name: name, Fingerprint: fingerprint, PublicKey: public, CreatedAt: timeOf(created)}
}

func (s accountStore) SSHKeys(ctx context.Context, userID uint64) ([]accountapi.SSHKeyRecord, error) {
	rows, err := s.q.AccountSSHKeys(ctx, uint32(userID))
	if err != nil {
		return nil, fmt.Errorf("read the SSH keys of user %d: %w", userID, err)
	}
	out := make([]accountapi.SSHKeyRecord, len(rows))
	for i, r := range rows {
		out[i] = sshRecord(r.ID, r.Name, r.Fingerprint, r.PublicKey, r.CreatedAt)
	}
	return out, nil
}

func (s accountStore) HasFingerprint(ctx context.Context, userID uint64, fingerprint string) (bool, error) {
	n, err := s.q.CountSSHKeysWithFingerprint(ctx, dbq.CountSSHKeysWithFingerprintParams{UserID: uint32(userID), Fingerprint: fingerprint})
	if err != nil {
		return false, fmt.Errorf("look for the SSH key: %w", err)
	}
	return n > 0, nil
}

// AddSSHKey stores a key unless the user has its fingerprint; the user's row is locked while that is looked at, so two requests cannot both add it.
func (s accountStore) AddSSHKey(ctx context.Context, userID uint64, k accountapi.SSHKeyRecord) (accountapi.SSHKeyRecord, error) {
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.LockUser(ctx, uint32(userID)); errors.Is(err, sql.ErrNoRows) {
			return accountapi.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock user %d: %w", userID, err)
		}
		n, err := q.CountSSHKeysWithFingerprint(ctx, dbq.CountSSHKeysWithFingerprintParams{UserID: uint32(userID), Fingerprint: k.Fingerprint})
		if err != nil {
			return fmt.Errorf("look for the SSH key: %w", err)
		}
		if n > 0 {
			return accountapi.ErrKeyExists
		}
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		id, err := q.InsertSSHKey(ctx, dbq.InsertSSHKeyParams{UserID: uint32(userID), Name: k.Name, Fingerprint: k.Fingerprint, PublicKey: k.PublicKey, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return fmt.Errorf("store the SSH key: %w", err)
		}
		k.ID, k.CreatedAt = uint64(id), now.Time
		return nil
	})
	return k, err
}

func (s accountStore) RemoveSSHKey(ctx context.Context, userID uint64, fingerprint string) (accountapi.SSHKeyRecord, bool, error) {
	var rec accountapi.SSHKeyRecord
	found := false
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		r, err := q.LockSSHKey(ctx, dbq.LockSSHKeyParams{UserID: uint32(userID), Fingerprint: fingerprint})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("find the SSH key: %w", err)
		}
		now := sql.NullTime{Time: s.now().UTC(), Valid: true}
		if err := q.SoftDeleteSSHKey(ctx, dbq.SoftDeleteSSHKeyParams{DeletedAt: now, UpdatedAt: now, ID: r.ID}); err != nil {
			return fmt.Errorf("remove the SSH key: %w", err)
		}
		rec, found = sshRecord(r.ID, r.Name, r.Fingerprint, r.PublicKey, r.CreatedAt), true
		return nil
	})
	return rec, found, err
}

// Log writes an entry about the user, who did it, and about the key if the event is about one.
func (s accountActivity) Log(ctx context.Context, e accountapi.Entry) error {
	var apiKey uint64
	if e.APIKeyID != nil {
		apiKey = *e.APIKeyID
	}
	en := entry{Event: e.Event, Subject: e.UserID, Actor: e.UserID, APIKeyID: apiKey, IP: e.IP, Properties: e.Properties}
	switch {
	case strings.HasPrefix(e.Event, "user:api-key."):
		en.Subjects = []subject{{apiKeyModel, e.SubjectID}}
	case strings.HasPrefix(e.Event, "user:ssh-key."):
		en.Subjects = []subject{{sshKeyModel, e.SubjectID}}
	}
	return s.logActivity(ctx, en)
}
