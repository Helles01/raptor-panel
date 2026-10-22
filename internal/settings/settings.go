// Package settings lists the panel settings an admin may change and validates their values.
//
// Settings are stored in the key/value `settings` table of the reference panel, under keys prefixed with "settings::".
// Secrets such as the SMTP password are not handled here: they are stored in raptor_secrets.
package settings

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// StoragePrefix is put in front of every key in the settings table.
const StoragePrefix = "settings::"

type kind int

const (
	kindString kind = iota
	kindInt
	kindBool
	kindEnum
	kindLocale
	kindEmail
)

type spec struct {
	kind     kind
	min, max int      // string length (characters) or integer range
	values   []string // for kindEnum
}

var (
	localePattern = regexp.MustCompile(`^[a-z]{2}$`)
	emailPattern  = regexp.MustCompile(`^[^\s@<>(),;:"]+@[^\s@<>(),;:"]+\.[^\s@<>(),;:"]+$`)
)

// specs returns the settings an admin can change, as in the reference panel's SettingsServiceProvider, without
// secrets and without the reCAPTCHA keys (reCAPTCHA is not implemented).
func specs() map[string]spec {
	return map[string]spec{
		"app:name":                           {kind: kindString, min: 1, max: 191},
		"app:locale":                         {kind: kindLocale},
		"pterodactyl:auth:2fa_required":      {kind: kindEnum, values: []string{"0", "1", "2"}}, // none, admins, everyone
		"pterodactyl:guzzle:timeout":         {kind: kindInt, min: 1, max: 600},
		"pterodactyl:guzzle:connect_timeout": {kind: kindInt, min: 1, max: 600},
		"pterodactyl:console:count":          {kind: kindInt, min: 1, max: 1000},
		"pterodactyl:console:frequency":      {kind: kindInt, min: 1, max: 60000},

		"pterodactyl:client_features:allocations:enabled":     {kind: kindBool},
		"pterodactyl:client_features:allocations:range_start": {kind: kindInt, min: 1024, max: 65535},
		"pterodactyl:client_features:allocations:range_end":   {kind: kindInt, min: 1024, max: 65535},

		"mail:mailers:smtp:host":       {kind: kindString, min: 1, max: 191},
		"mail:mailers:smtp:port":       {kind: kindInt, min: 1, max: 65535},
		"mail:mailers:smtp:encryption": {kind: kindEnum, values: []string{"", "tls", "ssl"}},
		"mail:mailers:smtp:username":   {kind: kindString, min: 0, max: 191},
		"mail:from:address":            {kind: kindEmail},
		"mail:from:name":               {kind: kindString, min: 1, max: 191},
	}
}

// Keys returns the names of all settings that can be changed, sorted.
func Keys() []string {
	var keys []string
	for k := range specs() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// StorageKey returns the key a setting has in the settings table.
func StorageKey(key string) string { return StoragePrefix + key }

// Validate checks that key is a known setting and value is acceptable for it.
func Validate(key, value string) error {
	s, ok := specs()[key]
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	if strings.ContainsFunc(value, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return fmt.Errorf("%s must not contain control characters", key)
	}

	switch s.kind {
	case kindString:
		if n := utf8.RuneCountInString(value); n < s.min || n > s.max {
			return fmt.Errorf("%s must be %d to %d characters", key, s.min, s.max)
		}
	case kindInt:
		n, err := strconv.Atoi(value)
		if err != nil || strconv.Itoa(n) != value || n < s.min || n > s.max {
			return fmt.Errorf("%s must be a whole number from %d to %d", key, s.min, s.max)
		}
	case kindBool:
		if value != "true" && value != "false" {
			return fmt.Errorf("%s must be true or false", key)
		}
	case kindEnum:
		for _, v := range s.values {
			if value == v {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of %q", key, s.values)
	case kindLocale:
		if !localePattern.MatchString(value) {
			return fmt.Errorf("%s must be a two letter language code such as en", key)
		}
	case kindEmail:
		if utf8.RuneCountInString(value) > 191 || !emailPattern.MatchString(value) {
			return fmt.Errorf("%s must be an e-mail address", key)
		}
	}
	return nil
}
