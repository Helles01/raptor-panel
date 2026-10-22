package settings

import (
	"sort"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{"app name", "app:name", "My Panel", ""},
		{"app name empty", "app:name", "", "1 to 191"},
		{"app name too long", "app:name", strings.Repeat("a", 192), "1 to 191"},
		{"app name with newline", "app:name", "a\nb", "control characters"},
		{"locale", "app:locale", "hu", ""},
		{"locale uppercase", "app:locale", "HU", "language code"},
		{"locale with path", "app:locale", "../", "language code"},

		{"2fa none", "pterodactyl:auth:2fa_required", "0", ""},
		{"2fa everyone", "pterodactyl:auth:2fa_required", "2", ""},
		{"2fa out of range", "pterodactyl:auth:2fa_required", "3", "one of"},
		{"2fa word", "pterodactyl:auth:2fa_required", "yes", "one of"},

		{"timeout", "pterodactyl:guzzle:timeout", "30", ""},
		{"timeout zero", "pterodactyl:guzzle:timeout", "0", "whole number"},
		{"timeout with plus sign", "pterodactyl:guzzle:timeout", "+30", "whole number"},
		{"timeout with space", "pterodactyl:guzzle:timeout", " 30", "whole number"},
		{"timeout not a number", "pterodactyl:guzzle:timeout", "soon", "whole number"},
		{"timeout too large", "pterodactyl:guzzle:timeout", "601", "whole number"},

		{"allocations enabled", "pterodactyl:client_features:allocations:enabled", "true", ""},
		{"allocations enabled as 1", "pterodactyl:client_features:allocations:enabled", "1", "true or false"},
		{"range start", "pterodactyl:client_features:allocations:range_start", "25565", ""},
		{"range start in the system ports", "pterodactyl:client_features:allocations:range_start", "80", "whole number"},

		{"smtp host", "mail:mailers:smtp:host", "smtp.example.com", ""},
		{"smtp port", "mail:mailers:smtp:port", "587", ""},
		{"smtp port too large", "mail:mailers:smtp:port", "70000", "whole number"},
		{"smtp encryption empty", "mail:mailers:smtp:encryption", "", ""},
		{"smtp encryption tls", "mail:mailers:smtp:encryption", "tls", ""},
		{"smtp encryption unknown", "mail:mailers:smtp:encryption", "starttls", "one of"},
		{"smtp username may be empty", "mail:mailers:smtp:username", "", ""},
		{"from address", "mail:from:address", "noreply@example.com", ""},
		{"from address invalid", "mail:from:address", "noreply", "e-mail"},
		{"from address with display name", "mail:from:address", "Panel <noreply@example.com>", "e-mail"},
		{"from name", "mail:from:name", "Panel", ""},

		{"unknown key", "app:debug", "true", "unknown setting"},
		{"the smtp password is not a plain setting", "mail:mailers:smtp:password", "secret", "unknown setting"},
		{"recaptcha is not implemented", "recaptcha:secret_key", "x", "unknown setting"},
		{"storage prefix is not part of the key", "settings::app:name", "x", "unknown setting"},
		{"empty key", "", "x", "unknown setting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.key, tt.value)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestKeys(t *testing.T) {
	keys := Keys()
	if !sort.StringsAreSorted(keys) || len(keys) != 16 {
		t.Errorf("Keys = %v", keys)
	}
	for _, k := range keys {
		if strings.Contains(k, "password") || strings.Contains(k, "secret") {
			t.Errorf("secret setting %q must not be changeable as a plain setting", k)
		}
		if err := Validate(k, "\x00"); err == nil {
			t.Errorf("%s accepted a NUL character", k)
		}
	}
}

func TestStorageKey(t *testing.T) {
	if got := StorageKey("app:name"); got != "settings::app:name" {
		t.Errorf("StorageKey = %q", got)
	}
}
