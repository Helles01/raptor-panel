package wings

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"

	"github.com/Helles01/raptor-panel/internal/ids"
	"github.com/Helles01/raptor-panel/internal/permissions"
)

// Scope says what a token may be used for. Wings reads it from the "scope" claim, and since Wings 1.12.2 refuses a
// token on an endpoint whose scope it does not carry. Without a purpose, a websocket token of a subuser could be used to
// upload files (GHSA-8r6w-3qq5-4p4r). Every token issued here has exactly one scope.
type Scope string

const (
	ScopeWebsocket      Scope = "websocket"
	ScopeFileUpload     Scope = "file-upload"
	ScopeFileDownload   Scope = "file-download"
	ScopeBackupDownload Scope = "backup-download"
)

// The subuser permission a user needs for each scope. The issuing functions check it, so a token cannot be made for
// something its user could not do through the panel.
const (
	PermissionWebsocket      = permissions.WebsocketConnect
	PermissionFileUpload     = permissions.FileCreate
	PermissionFileDownload   = permissions.FileReadContent
	PermissionBackupDownload = permissions.BackupDownload
)

// Lifetimes of the tokens, as in the reference: short, because Wings cannot revoke a signed URL before it expires.
const (
	websocketTTL = 10 * time.Minute
	transferTTL  = 15 * time.Minute
	// notBeforeSkew lets a node whose clock is a little behind accept a fresh token.
	notBeforeSkew = 5 * time.Minute

	maxFilePathLength = 4096
)

// ErrMissingPermission is returned, and no token is made, when the subject lacks the permission of the scope.
var ErrMissingPermission = errors.New("wings: the user does not have the permission this token needs")

// Subject is the user a token is made for, with what they may do on the server.
type Subject interface {
	// ID is the numeric user id; it is part of the token identifier, which Wings uses to revoke tokens.
	ID() uint64
	// UUID is the user's UUID, the "user_uuid" claim.
	UUID() string
	// Can reports whether the user has a subuser permission on the server (owners and admins have all of them).
	Can(permission string) bool
	// Permissions is the effective permission list sent to the websocket ("*" for owners and admins).
	Permissions() []string
}

// Issuer makes the tokens a node accepts. Wings checks the signature with the node's own token, the expiry, and its
// revocation list; the panel never receives these tokens back.
type Issuer struct {
	appURL string
	now    func() time.Time
	rand   io.Reader
}

// NewIssuer returns an Issuer. appURL is the panel's public URL, the issuer ("iss") of every token.
func NewIssuer(appURL string) (*Issuer, error) {
	u, err := url.Parse(appURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("wings: the panel URL must be an http(s) address")
	}
	return &Issuer{appURL: appURL, now: time.Now, rand: rand.Reader}, nil
}

type claims struct {
	Iss string   `json:"iss"`
	Aud []string `json:"aud"`
	Jti string   `json:"jti"`
	Iat int64    `json:"iat"`
	Nbf int64    `json:"nbf"`
	Exp int64    `json:"exp"`

	Scope      string `json:"scope"`
	UserUUID   string `json:"user_uuid"`
	UniqueID   string `json:"unique_id"`
	ServerUUID string `json:"server_uuid"`

	Permissions *[]string `json:"permissions,omitempty"`
	FilePath    string    `json:"file_path,omitempty"`
	BackupUUID  string    `json:"backup_uuid,omitempty"`
}

// Websocket makes the token for a server's console and live statistics. The user needs websocket.connect.
func (i *Issuer) Websocket(n Node, who Subject, serverUUID string) (string, error) {
	perms := append([]string{}, who.Permissions()...)
	return i.issue(n, who, serverUUID, ScopeWebsocket, PermissionWebsocket, websocketTTL, func(c *claims) { c.Permissions = &perms })
}

// FileUpload makes the token for uploading files to a server. The user needs file.create.
func (i *Issuer) FileUpload(n Node, who Subject, serverUUID string) (string, error) {
	return i.issue(n, who, serverUUID, ScopeFileUpload, PermissionFileUpload, transferTTL, func(*claims) {})
}

// FileDownload makes the token for downloading one file of a server. The user needs file.read-content.
func (i *Issuer) FileDownload(n Node, who Subject, serverUUID, filePath string) (string, error) {
	if filePath == "" || len(filePath) > maxFilePathLength || hasControl(filePath) {
		return "", errors.New("wings: the file path is empty, too long or contains control characters")
	}
	return i.issue(n, who, serverUUID, ScopeFileDownload, PermissionFileDownload, transferTTL, func(c *claims) { c.FilePath = filePath })
}

// BackupDownload makes the token for downloading a backup of a server. The user needs backup.download.
func (i *Issuer) BackupDownload(n Node, who Subject, serverUUID, backupUUID string) (string, error) {
	if err := checkUUID("the backup", backupUUID); err != nil {
		return "", err
	}
	return i.issue(n, who, serverUUID, ScopeBackupDownload, PermissionBackupDownload, transferTTL, func(c *claims) { c.BackupUUID = backupUUID })
}

// issue is the only place a token is built. It needs a scope and the permission that goes with it, so there is no way to
// make a token without a purpose or for something the user may not do.
func (i *Issuer) issue(n Node, who Subject, serverUUID string, scope Scope, permission string, ttl time.Duration, extra func(*claims)) (string, error) {
	if scope == "" {
		return "", errors.New("wings: a token needs a scope")
	}
	if err := n.Validate(); err != nil {
		return "", err
	}
	if err := checkUUID("the server", serverUUID); err != nil {
		return "", err
	}
	if err := checkUUID("the user", who.UUID()); err != nil {
		return "", err
	}
	if !who.Can(permission) {
		return "", fmt.Errorf("%w (%s)", ErrMissingPermission, permission)
	}

	uniqueID, err := ids.RandomAlnum(i.rand, 16)
	if err != nil {
		return "", err
	}
	// The identifier ties a token to a user and a server, so Wings can refuse all tokens of one user on one server.
	identity := sha256.Sum256([]byte(strconv.FormatUint(who.ID(), 10) + serverUUID))

	now := i.now()
	c := claims{
		Iss: i.appURL, Aud: []string{n.Address()}, Jti: hex.EncodeToString(identity[:]),
		Iat: now.Unix(), Nbf: now.Add(-notBeforeSkew).Unix(), Exp: now.Add(ttl).Unix(),
		Scope: string(scope), UserUUID: who.UUID(), UniqueID: uniqueID, ServerUUID: serverUUID,
	}
	extra(&c)

	return sign(n.Token, c)
}

// sign builds a compact JWT with HMAC-SHA256.
func sign(key string, c claims) (string, error) {
	enc := base64.RawURLEncoding
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode token: %w", err)
	}

	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(signingInput))
	return signingInput + "." + enc.EncodeToString(mac.Sum(nil)), nil
}
