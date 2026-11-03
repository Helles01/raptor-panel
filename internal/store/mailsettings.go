package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/settings"
)

// smtpPassword is the setting whose value is the password of the mail server. It is not in the settings of the panel that can be set from the
// command line: it comes from the old panel with raptor migrate-secrets, sealed in raptor_secrets under the row of the setting.
const smtpPassword = "mail:mailers:smtp:password"

// MailSettings is what the panel sends mail with.
func (d *DB) MailSettings() mail.Settings { return mailSettings{d} }

type mailSettings struct{ *DB }

func (s mailSettings) Mail(ctx context.Context) (map[string]string, string, error) {
	values := map[string]string{}
	for _, key := range mail.SettingKeys() {
		v, err := s.q.SettingValue(ctx, settings.StorageKey(key))
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return nil, "", fmt.Errorf("read setting %s: %w", key, err)
		default:
			values[key] = v
		}
	}
	id, err := s.q.SettingRowID(ctx, settings.StorageKey(smtpPassword))
	if errors.Is(err, sql.ErrNoRows) {
		return values, "", nil
	} else if err != nil {
		return nil, "", fmt.Errorf("find the setting of the mail password: %w", err)
	}
	password, _, err := s.secret(ctx, "settings", uint64(id), "value")
	if err != nil {
		return nil, "", err
	}
	return values, password, nil
}
