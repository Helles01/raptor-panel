// Package wings is the panel's side of the conversation with the Wings node daemons: the signed tokens it gives to
// browsers (websocket, file transfers) and the HTTP client it uses to control servers.
package wings

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const minTokenLength = 32

var (
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Node is how to reach one Wings daemon, and the secret that authenticates the panel to it and signs its tokens.
type Node struct {
	Scheme string // "http" or "https"
	FQDN   string
	Port   int
	// Token is the decrypted daemon token. Never log it; Node's String, GoString and LogValue leave it out.
	Token string
}

// Validate checks the address and the token.
func (n Node) Validate() error {
	switch {
	case n.Scheme != "http" && n.Scheme != "https":
		return errors.New("wings node: the scheme must be http or https")
	case !validHost(n.FQDN):
		return errors.New("wings node: the host must be a host name or an IP address")
	case n.Port < 1 || n.Port > 65535:
		return errors.New("wings node: the port must be between 1 and 65535")
	case len(n.Token) < minTokenLength:
		return fmt.Errorf("wings node: the token must be at least %d characters", minTokenLength)
	}
	return nil
}

func validHost(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	return net.ParseIP(host) != nil || hostPattern.MatchString(host)
}

// Address returns scheme://host:port, which is what the reference calls the connection address and what the tokens
// name as their audience.
func (n Node) Address() string {
	return n.Scheme + "://" + net.JoinHostPort(n.FQDN, strconv.Itoa(n.Port))
}

func (n Node) socketAddress() string {
	switch n.Scheme {
	case "https":
		return "wss://" + net.JoinHostPort(n.FQDN, strconv.Itoa(n.Port))
	default:
		return "ws://" + net.JoinHostPort(n.FQDN, strconv.Itoa(n.Port))
	}
}

// WebsocketURL is where the browser connects the console of a server.
func (n Node) WebsocketURL(serverUUID string) string {
	return n.socketAddress() + "/api/servers/" + serverUUID + "/ws"
}

// UploadURL is the address a browser uploads files to, with the signed token.
func (n Node) UploadURL(token string) string {
	return n.Address() + "/upload/file?token=" + url.QueryEscape(token)
}

// FileDownloadURL is the address a browser downloads a file from.
func (n Node) FileDownloadURL(token string) string {
	return n.Address() + "/download/file?token=" + url.QueryEscape(token)
}

// BackupDownloadURL is the address a browser downloads a backup from.
func (n Node) BackupDownloadURL(token string) string {
	return n.Address() + "/download/backup?token=" + url.QueryEscape(token)
}

// String never includes the token.
func (n Node) String() string { return fmt.Sprintf("node{%s token=[redacted]}", n.Address()) }

// GoString keeps %#v from printing the token.
func (n Node) GoString() string { return n.String() }

// LogValue keeps slog from printing the token.
func (n Node) LogValue() slog.Value { return slog.StringValue(n.String()) }

func checkUUID(what, value string) error {
	if !uuidPattern.MatchString(value) {
		return fmt.Errorf("%s is not a UUID", what)
	}
	return nil
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f })
}
