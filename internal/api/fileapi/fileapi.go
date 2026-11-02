// Package fileapi serves the file manager of a server: listing a directory, reading and writing files, moving, copying, packing and
// deleting them, pulling a file from the internet, and the signed links with which a browser downloads and uploads straight to the
// node. The rules are in internal/files; this package puts them in order and talks to the node and the activity log through the
// interfaces below. The node keeps every path inside the server's directory; the panel checks the form of what it passes on.
package fileapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/files"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// Daemon is the calls to a node that these routes make. NewDaemon is the one that talks to a real Wings.
type Daemon interface {
	List(ctx context.Context, n wings.Node, serverUUID, directory string) ([]wings.FileObject, error)
	Read(ctx context.Context, n wings.Node, serverUUID, path string, max int64) ([]byte, error)
	Write(ctx context.Context, n wings.Node, serverUUID, path string, content []byte) error
	CreateDirectory(ctx context.Context, n wings.Node, serverUUID, name, path string) error
	Rename(ctx context.Context, n wings.Node, serverUUID, root string, items []wings.RenameItem) error
	Copy(ctx context.Context, n wings.Node, serverUUID, location string) error
	Delete(ctx context.Context, n wings.Node, serverUUID, root string, names []string) error
	Compress(ctx context.Context, n wings.Node, serverUUID, root string, names []string) (wings.FileObject, error)
	Decompress(ctx context.Context, n wings.Node, serverUUID, root, file string) error
	Chmod(ctx context.Context, n wings.Node, serverUUID, root string, items []wings.ChmodItem) error
	Pull(ctx context.Context, n wings.Node, serverUUID string, o wings.PullOptions) error
}

// Tokens makes the tokens with which a browser downloads a file from a node, or uploads files to it. *wings.Issuer is one.
type Tokens interface {
	FileDownload(n wings.Node, who wings.Subject, serverUUID, filePath string) (string, error)
	FileUpload(n wings.Node, who wings.Subject, serverUUID string) (string, error)
}

// Entry is an entry of the activity log about a server.
type Entry struct {
	Event string
	// ServerID is the server the entry is about; ActorID the user who did it. APIKeyID is set when the call came with an API key.
	ServerID   uint64
	ActorID    uint64
	APIKeyID   *uint64
	IP         string
	Properties string
}

// ActivityLog stores activity entries.
type ActivityLog interface {
	Log(ctx context.Context, e Entry) error
}

// The throttle of pulling files: five in ten minutes for each server, whoever pulls, as in the reference.
const (
	PullLimit  = 5
	PullWindow = 10 * time.Minute
)

// DefaultMaxEditSize is the largest file the editor opens and the largest content it saves, 4 MiB as in the reference.
const DefaultMaxEditSize = 4 << 20

// Deps are everything the endpoints need.
type Deps struct {
	Targets  controlapi.Targets
	Daemon   Daemon
	Tokens   Tokens
	Activity ActivityLog
	// PullLimiter allows PullLimit pulls in PullWindow for a server.
	PullLimiter *middleware.Limiter
	// MaxEditSize defaults to DefaultMaxEditSize.
	MaxEditSize int64
	// Location is the time zone of the panel, UTC when nil.
	Location *time.Location
	Logger   *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Targets == nil || d.Daemon == nil || d.Tokens == nil || d.Activity == nil || d.PullLimiter == nil || d.Logger == nil {
		return nil, errors.New("fileapi: the target store, the daemon, the token maker, the activity log, the limiter and a logger are required")
	}
	if d.MaxEditSize <= 0 {
		d.MaxEditSize = DefaultMaxEditSize
	}
	if d.Location == nil {
		d.Location = time.UTC
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Handlers{d: d}, nil
}

// permission is the permission of an operation, from the one table of the reference's.
func permission(op files.Operation) api.Permission {
	p, ok := files.PermissionFor(op)
	if !ok {
		panic("fileapi: no permission for " + string(op))
	}
	return api.ServerPermission(p)
}

// Register adds the routes below /api/client/servers/{server}/files.
func (h *Handlers) Register(g api.Group) {
	f := g.Sub("/api/client/servers/:server/files")
	f.GET("/list", h.List, permission(files.OpList))
	f.GET("/contents", h.Contents, permission(files.OpContents))
	f.GET("/download", h.Download, permission(files.OpDownload))
	f.PUT("/rename", h.Rename, permission(files.OpRename))
	f.POST("/copy", h.Copy, permission(files.OpCopy))
	f.POST("/write", h.Write, permission(files.OpWrite))
	f.POST("/compress", h.Compress, permission(files.OpCompress))
	f.POST("/decompress", h.Decompress, permission(files.OpDecompress))
	f.POST("/delete", h.Delete, permission(files.OpDelete))
	f.POST("/create-folder", h.CreateFolder, permission(files.OpCreateDirectory))
	f.POST("/chmod", h.Chmod, permission(files.OpChmod))
	f.POST("/pull", h.Pull, permission(files.OpPull))
	f.GET("/upload", h.Upload, permission(files.OpUpload))
}

func caller(c echo.Context) (authz.Principal, error) {
	p, ok := authz.From(c)
	if !ok || p.Server == nil {
		return authz.Principal{}, apierr.Forbidden("This action is unauthorized.")
	}
	return p, nil
}

// subject is the caller as the token maker sees them.
type subject struct {
	id   uint64
	uuid string
	held []string
}

func (s subject) ID() uint64                 { return s.id }
func (s subject) UUID() string               { return s.uuid }
func (s subject) Can(permission string) bool { return permissions.Has(s.held, permission) }
func (s subject) Permissions() []string      { return s.held }
