// Package nodes validates locations and nodes, creates node credentials and renders the Wings configuration file.
// The rules follow the reference panel's Node and Location models.
package nodes

import (
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/ids"
)

const (
	// TokenIDLength and TokenLength are the sizes of the credentials Wings uses to authenticate to the panel.
	TokenIDLength = 16
	TokenLength   = 64

	maxResource = 100_000_000 // MB; a sanity limit, far above any real node
	maxUpload   = 1 << 20     // MB
)

var (
	namePattern       = regexp.MustCompile(`^[\w .-]{1,100}$`)
	daemonBasePattern = regexp.MustCompile(`^/[\d\w.\-/]+$`)
	hostnamePattern   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
)

// ValidateLocation checks the short and long names of a location.
func ValidateLocation(short, long string) error {
	if n := utf8.RuneCountInString(short); n < 1 || n > 60 {
		return errors.New("short name must be 1 to 60 characters")
	}
	if utf8.RuneCountInString(long) > 191 {
		return errors.New("long name must be at most 191 characters")
	}
	for _, s := range []string{short, long} {
		if strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			return errors.New("location names must not contain control characters")
		}
	}
	return nil
}

// NewNode is what an admin provides for a new node. Memory and disk are in MB; an overallocation of -1 means
// unlimited, 0 means none.
type NewNode struct {
	Name               string
	Description        string
	LocationID         uint64
	FQDN               string
	Scheme             string // http or https
	BehindProxy        bool
	Public             bool
	Memory             int64
	MemoryOverallocate int64
	Disk               int64
	DiskOverallocate   int64
	DaemonBase         string
	DaemonSFTP         int // port
	DaemonListen       int // port
	UploadSize         int64
	MaintenanceMode    bool
}

// DefaultNewNode returns the defaults of the reference panel's new node form.
func DefaultNewNode() NewNode {
	return NewNode{
		Scheme:       "https",
		Public:       true,
		DaemonBase:   "/var/lib/pterodactyl/volumes",
		DaemonSFTP:   2022,
		DaemonListen: 8080,
		UploadSize:   100,
	}
}

// Validate checks every field against simple allow-lists. It does not resolve the host name.
func (n NewNode) Validate() error {
	switch {
	case !namePattern.MatchString(n.Name):
		return errors.New("name must be 1 to 100 characters of letters, digits, spaces, dots, dashes and underscores")
	case utf8.RuneCountInString(n.Description) > 10000 || strings.ContainsRune(n.Description, 0):
		return errors.New("description is too long or contains a NUL character")
	case n.LocationID < 1:
		return errors.New("location is required")
	case !validHost(n.FQDN):
		return errors.New("fqdn must be a host name or an IP address")
	case n.Scheme != "http" && n.Scheme != "https":
		return errors.New("scheme must be http or https")
	case n.Memory < 1 || n.Memory > maxResource:
		return errors.New("memory must be at least 1 MB")
	case n.MemoryOverallocate < -1 || n.MemoryOverallocate > maxResource:
		return errors.New("memory overallocation must be -1 (unlimited) or more")
	case n.Disk < 1 || n.Disk > maxResource:
		return errors.New("disk must be at least 1 MB")
	case n.DiskOverallocate < -1 || n.DiskOverallocate > maxResource:
		return errors.New("disk overallocation must be -1 (unlimited) or more")
	case !validDaemonBase(n.DaemonBase):
		return errors.New("daemon base must be an absolute path of letters, digits, dots, dashes, underscores and slashes, without .. segments")
	case n.DaemonSFTP < 1 || n.DaemonSFTP > 65535:
		return errors.New("SFTP port must be between 1 and 65535")
	case n.DaemonListen < 1 || n.DaemonListen > 65535:
		return errors.New("daemon port must be between 1 and 65535")
	case n.UploadSize < 1 || n.UploadSize > maxUpload:
		return errors.New("upload size must be at least 1 MB")
	}
	return nil
}

func validHost(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	return hostnamePattern.MatchString(host)
}

func validDaemonBase(p string) bool {
	if !daemonBasePattern.MatchString(p) {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// Credentials authenticate a Wings node to the panel: the node sends "token_id.token" as a bearer token.
type Credentials struct {
	TokenID string
	Token   string
}

// NewCredentials generates the token ID (16 characters) and the token (64 characters) of a node.
func NewCredentials(r io.Reader) (Credentials, error) {
	id, err := ids.RandomAlnum(r, TokenIDLength)
	if err != nil {
		return Credentials{}, fmt.Errorf("generate token id: %w", err)
	}
	token, err := ids.RandomAlnum(r, TokenLength)
	if err != nil {
		return Credentials{}, fmt.Errorf("generate token: %w", err)
	}
	return Credentials{TokenID: id, Token: token}, nil
}
