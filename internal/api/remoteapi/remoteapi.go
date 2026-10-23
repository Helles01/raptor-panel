// Package remoteapi serves the API a node (Wings) calls: the SFTP sign-in, the list and configuration of its servers, the report of an
// installation, the activity it collected, the state reset after its restart, and the end of a transfer. The rules are in
// internal/sftp, internal/remote, internal/servers and internal/activity; this package puts them in order and reads and writes
// through the interfaces below.
//
// Every lookup of a server here is bound to the calling node: a store never finds another node's server for a node, except what
// the transfer rules of internal/remote allow. The handlers still apply those rules to what the store returns, so a store that
// forgets the binding is caught by the tests of its own.
package remoteapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/remote"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// ErrNotFound is what a store answers when there is no such record for the node.
var ErrNotFound = errors.New("remoteapi: not found")

// SFTPUser is a user who signs in by SFTP.
type SFTPUser struct {
	ID           uint64
	UUID         string
	Admin        bool
	PasswordHash string
}

// SFTPServer is a server of the calling node, with what the signing-in user holds on it.
type SFTPServer struct {
	ID      uint64
	UUID    string
	NodeID  uint64
	OwnerID uint64
	State   servers.State
	// Subuser holds the permissions of the user's subuser entry on the server, nil without one.
	Subuser []string
}

// Users is the part of the user store the SFTP sign-in needs.
type Users interface {
	// ByUsername finds a user by user name, which the database compares without regard to case.
	ByUsername(ctx context.Context, username string) (SFTPUser, error)
	// Fingerprints returns the fingerprints of the SSH keys a user registered.
	Fingerprints(ctx context.Context, userID uint64) ([]string, error)
}

// ServerPage is one page of the servers of a node.
type ServerPage struct {
	Total   int
	Servers []servers.Server
}

// Detail is a server with its node and its active transfer, if any.
type Detail struct {
	NodeID   uint64
	Transfer *remote.Transfer
	Server   servers.Server
}

// Held is a server of the calling node as the installation report needs it.
type Held struct {
	ID              uint64
	UUID            string
	NodeID          uint64
	Name            string
	Status          string
	InstalledBefore bool
	OwnerEmail      string
	OwnerUsername   string
}

// InstallScript is what the node runs to install a server.
type InstallScript struct {
	ContainerImage string `json:"container_image"`
	Entrypoint     string `json:"entrypoint"`
	Script         string `json:"script"`
}

// BackupRef names the backup a server was restoring.
type BackupRef struct {
	ID   uint64
	Name string
}

// ResetCandidate is a server of the node that is installing or restoring a backup.
type ResetCandidate struct {
	ID              uint64
	Status          string
	InstalledBefore bool
	// Restoring is the backup being restored, nil if the store knows of none.
	Restoring *BackupRef
}

// ResetChange is the new status of a server whose node restarted. RestoreFailed is set for a restore that did not finish.
type ResetChange struct {
	ServerID uint64
	From, To string
	// RestoreFailed makes the store write a server:backup.restore-failed entry for the backup, in the same transaction.
	RestoreFailed *BackupRef
}

// TransferInfo is a server of the node (or one moving to or from it) and its active transfer; Transfer is nil when there is none.
type TransferInfo struct {
	ServerID uint64
	Transfer *remote.Transfer
}

// Servers is the part of the server store the node API needs. Every method takes the node and finds only what it may reach.
type Servers interface {
	// SFTP finds a server of the node by the first eight characters of its UUID, with the user's subuser entry.
	SFTP(ctx context.Context, nodeID uint64, shortID string, userID uint64) (SFTPServer, error)
	// List returns one page of the node's servers, in a fixed order, with everything their configuration needs.
	List(ctx context.Context, nodeID uint64, offset, limit int) (ServerPage, error)
	// Reachable finds a server that is on the node, or that is being transferred to or from it.
	Reachable(ctx context.Context, nodeID uint64, uuid string) (Detail, error)
	// Own finds a server that is on the node.
	Own(ctx context.Context, nodeID uint64, uuid string) (Held, error)
	InstallScript(ctx context.Context, serverID uint64) (InstallScript, error)
	// Installed stores the result of an installation, only if the server still has the status from, and says whether it did.
	Installed(ctx context.Context, serverID uint64, from string, o servers.InstallOutcome, at time.Time) (bool, error)
	// Resettable lists the servers of the node that are installing or restoring a backup.
	Resettable(ctx context.Context, nodeID uint64) ([]ResetCandidate, error)
	// Reset applies the changes in one transaction. A server that no longer has the status From is left alone.
	Reset(ctx context.Context, nodeID uint64, changes []ResetChange) error
	// Transfer finds a server that is on the node or being transferred to or from it, with its active transfer.
	Transfer(ctx context.Context, nodeID uint64, uuid string) (TransferInfo, error)
	// CompleteTransfer stores the end of a transfer in one transaction, only if it is still running, and says whether it did.
	CompleteTransfer(ctx context.Context, serverID uint64, r remote.TransferResult) (bool, error)
}

// StoredEntry is an event of a node, ready to store.
type StoredEntry struct {
	ServerID uint64
	// UserID is 0 for an event with no user.
	UserID     uint64
	Event      string
	IP         string
	Properties string
	When       time.Time
}

// ActivitySink stores the activity a node reports.
type ActivitySink interface {
	// Servers returns the ids of those of the UUIDs that are servers of the node.
	Servers(ctx context.Context, nodeID uint64, uuids []string) (map[string]uint64, error)
	// Users returns the ids of those of the UUIDs that are users.
	Users(ctx context.Context, uuids []string) (map[string]uint64, error)
	// Store writes the entries and their subject rows in one transaction.
	Store(ctx context.Context, entries []StoredEntry) error
}

// Event is an entry of the activity log about a sign-in or a server, written by the panel.
type Event struct {
	Name string
	// UserSubject and ServerSubject are the records the entry is about, 0 for none; Actor is the user who did it, 0 for none.
	UserSubject   uint64
	ServerSubject uint64
	Actor         uint64
	IP            string
	// Properties is the JSON object from activity.Properties.
	Properties string
}

// EventLog stores events of the panel.
type EventLog interface {
	Log(ctx context.Context, e Event) error
}

// Outbox takes mail to be sent later.
type Outbox interface {
	Send(ctx context.Context, m mail.Message) error
}

// OldNodes deletes a server from the node it was moved away from.
type OldNodes interface {
	DeleteServer(ctx context.Context, nodeID uint64, serverUUID string) error
}

// Deps are everything the endpoints need.
type Deps struct {
	Servers  Servers
	Users    Users
	Activity ActivitySink
	Events   EventLog
	Outbox   Outbox
	OldNodes OldNodes

	// Failures counts the failed SFTP sign-ins.
	Failures *middleware.Tracker
	Site     mail.Site
	// Notify says which finished installations the owner is told about.
	Notify servers.NotifyConfig
	Logger *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	switch {
	case d.Servers == nil || d.Users == nil || d.Activity == nil || d.Events == nil || d.Outbox == nil || d.OldNodes == nil:
		return nil, errors.New("remoteapi: every store is required")
	case d.Failures == nil || d.Logger == nil:
		return nil, errors.New("remoteapi: the failure tracker and a logger are required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Handlers{d: d}, nil
}

// Register adds the routes below /api/remote.
func (h *Handlers) Register(g api.Group) {
	r := g.Sub("/api/remote")
	r.POST("/sftp/auth", h.SFTPAuth, api.Daemon())
	r.GET("/servers", h.List, api.Daemon())
	r.POST("/servers/reset", h.Reset, api.Daemon())
	r.POST("/activity", h.Activity, api.Daemon())

	s := r.Sub("/servers/:uuid")
	s.GET("", h.Details, api.Daemon())
	s.GET("/install", h.InstallScript, api.Daemon())
	s.POST("/install", h.InstallReport, api.Daemon())
	s.POST("/transfer/failure", h.TransferFailure, api.Daemon())
	s.POST("/transfer/success", h.TransferSuccess, api.Daemon())
}

// scope returns the calling node. A route wired without the authorizer has none; it is refused rather than served.
func scope(c echo.Context) (remote.Scope, error) {
	n, ok := authz.NodeFrom(c)
	if !ok || n.ID == 0 {
		return remote.Scope{}, apierr.Forbidden(remote.ErrForbidden.Error())
	}
	return remote.Scope{NodeID: n.ID}, nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// serverUUID reads the {uuid} of the route. The UUID is a lookup key, so what cannot be one is not looked up.
func serverUUID(c echo.Context) (string, error) {
	uuid := strings.ToLower(c.Param("uuid"))
	if !uuidPattern.MatchString(uuid) {
		return "", apierr.NotFound()
	}
	return uuid, nil
}

// forbidden is the answer for a server the node may not reach, whether it belongs to another node or does not exist, so that a node
// learns nothing about the servers of others.
func forbidden() error { return apierr.Forbidden(remote.ErrForbidden.Error()) }

// found turns what a node-bound lookup answered into an error: nothing found is the refusal above, a failure is a 500.
func found(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return forbidden()
	}
	return apierr.Internal(err)
}

// logEvent writes an event of the panel. A failure is logged and never reaches the node.
func (h *Handlers) logEvent(c echo.Context, e Event) {
	if err := h.d.Events.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", e.Name, "error", err)
	}
}

// properties builds the properties of an event, or logs why it could not and returns "".
func (h *Handlers) properties(event string, values map[string]any) (string, bool) {
	p, err := activity.Properties(event, values)
	if err != nil {
		h.d.Logger.Error("activity entry refused", "event", event, "error", err)
		return "", false
	}
	return p, true
}

// clientHost is the address of the person at the other end of a connection the node reports, without the port. The node
// sends its own address in the request, which says nothing about them.
func clientHost(reported, fallback string) string {
	if host, _, err := net.SplitHostPort(reported); err == nil && net.ParseIP(host) != nil {
		return host
	}
	if net.ParseIP(reported) != nil {
		return reported
	}
	return fallback
}

func noContent(c echo.Context) error { return c.NoContent(http.StatusNoContent) }
