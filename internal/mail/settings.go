package mail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ErrNotConfigured is what OptionsFromSettings answers when the panel has no mail server set up: the host is not there. The panel
// then sends no mail, and says so, rather than failing.
var ErrNotConfigured = errors.New("mail: no mail server is set up")

// The settings of the panel that say how mail is sent (internal/settings). The password is not one of them: it is a secret, and comes
// from the configuration of the process.
const (
	settingHost       = "mail:mailers:smtp:host"
	settingPort       = "mail:mailers:smtp:port"
	settingEncryption = "mail:mailers:smtp:encryption"
	settingUsername   = "mail:mailers:smtp:username"
	settingFrom       = "mail:from:address"
)

// OptionsFromSettings makes the options of a Sender from the settings of the panel. get returns the value of a setting, empty when it
// has none. The reference's values of "encryption" are read as it reads them: "tls" is STARTTLS, "ssl" is TLS from the first byte, and
// nothing is no protection, which NewSender then allows only for this machine, or for a server that needs no password.
func OptionsFromSettings(get func(key string) string, password, hello string) (SenderOptions, error) {
	host := strings.TrimSpace(get(settingHost))
	if host == "" {
		return SenderOptions{}, ErrNotConfigured
	}
	port := strings.TrimSpace(get(settingPort))
	if port == "" {
		return SenderOptions{}, errors.New("mail: the port of the mail server is not set")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return SenderOptions{}, fmt.Errorf("mail: %q is not a port", port)
	}

	var security Security
	switch enc := strings.ToLower(strings.TrimSpace(get(settingEncryption))); enc {
	case "tls":
		security = StartTLS
	case "ssl":
		security = ImplicitTLS
	case "":
		security = NoTLS
	default:
		return SenderOptions{}, fmt.Errorf("mail: %q is not a way to protect the connection (tls, ssl or none)", enc)
	}
	return SenderOptions{
		Addr: net.JoinHostPort(host, port), Username: get(settingUsername), Password: password, Security: security, From: strings.TrimSpace(get(settingFrom)), Hello: hello,
	}, nil
}

// SenderFromSettings is OptionsFromSettings and NewSender in one.
func SenderFromSettings(get func(key string) string, password, hello string) (*Sender, error) {
	o, err := OptionsFromSettings(get, password, hello)
	if err != nil {
		return nil, err
	}
	return NewSender(o)
}

// SettingKeys are the settings OptionsFromSettings reads, for a store that has to fetch them.
func SettingKeys() []string {
	return []string{settingHost, settingPort, settingEncryption, settingUsername, settingFrom}
}

// Settings is where the panel keeps what it sends mail with.
type Settings interface {
	// Mail returns the values of the settings named by SettingKeys (a setting that is not set is missing from the map) and the password of the mail
	// server, opened; the password is empty when none is stored.
	Mail(ctx context.Context) (values map[string]string, password string, err error)
}
