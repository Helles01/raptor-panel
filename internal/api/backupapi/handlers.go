package backupapi

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/permissions"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/wings"
)

const (
	listPerPage    = 20
	maxListPerPage = 50
	// maxBody holds a full list of files to leave out (32 KiB) in JSON.
	maxBody = 128 << 10

	// atom is how the reference writes a time (Carbon's toAtomString): the offset as +00:00, never Z.
	atom = "2006-01-02T15:04:05-07:00"
)

// errUnsupportedDisk is for a backup that is kept somewhere this panel is not set up to reach: S3 without the S3 settings, or a disk nobody knows (C-060).
var errUnsupportedDisk = errors.New("This backup is stored on a disk that this panel is not set up to use: a backup kept on S3 needs the S3 settings of the panel.")

type backupAttributes struct {
	UUID         string   `json:"uuid"`
	IsSuccessful bool     `json:"is_successful"`
	IsLocked     bool     `json:"is_locked"`
	Name         string   `json:"name"`
	IgnoredFiles []string `json:"ignored_files"`
	Checksum     *string  `json:"checksum"`
	Bytes        int64    `json:"bytes"`
	CreatedAt    string   `json:"created_at"`
	CompletedAt  *string  `json:"completed_at"`
}

func (h *Handlers) item(r Record) resp.Item {
	ignored := r.IgnoredFiles
	if ignored == nil {
		ignored = []string{}
	}
	var completed *string
	if r.CompletedAt != nil {
		s := r.CompletedAt.In(h.d.Location).Format(atom)
		completed = &s
	}
	return resp.NewItem("backup", backupAttributes{
		UUID: r.UUID, IsSuccessful: r.Successful, IsLocked: r.Locked, Name: r.Name, IgnoredFiles: ignored, Checksum: r.Checksum, Bytes: r.Bytes,
		CreatedAt: r.CreatedAt.In(h.d.Location).Format(atom), CompletedAt: completed,
	})
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// find reads the {backup} of the route, a UUID, among the backups of the server.
func (h *Handlers) find(c echo.Context, p authz.Principal) (Record, error) {
	uuid := strings.ToLower(c.Param("backup"))
	if !uuidPattern.MatchString(uuid) {
		return Record{}, apierr.NotFound()
	}
	r, err := h.d.Store.Find(c.Request().Context(), p.Server.ID, uuid)
	if err != nil {
		return Record{}, storeError(err)
	}
	return r, nil
}

// log writes an entry about a backup. A failure is logged and never reaches the person, whose action has happened.
func (h *Handlers) log(c echo.Context, p authz.Principal, event string, r Record, values map[string]any) {
	props, err := activity.Properties(event, values)
	if err != nil {
		h.d.Logger.Error("activity entry refused", "event", event, "error", err)
		return
	}
	e := Entry{Event: event, ServerID: p.Server.ID, BackupID: r.ID, ActorID: p.User.ID, IP: c.RealIP(), Properties: props}
	if p.Key != nil {
		id := p.Key.ID
		e.APIKeyID = &id
	}
	if err := h.d.Activity.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "server", p.Server.ID, "error", err)
	}
}

// List is GET /backups: a page of the backups, 20 by default and at most 50, with the number that count against the limit.
func (h *Handlers) List(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	page, perPage, err := resp.PageParamsWith(c.QueryParams(), listPerPage, maxListPerPage)
	if err != nil {
		return err
	}
	result, err := h.d.Store.Page(c.Request().Context(), p.Server.ID, (page-1)*perPage, perPage)
	if err != nil {
		return storeError(err)
	}
	items := make([]resp.Item, len(result.Backups))
	for i, r := range result.Backups {
		items[i] = h.item(r)
	}
	list := resp.NewList(items).
		WithPagination(resp.NewPagination(c.Request().URL, result.Total, perPage, page, len(items))).
		WithMeta("backup_count", result.NonFailed)
	return c.JSON(http.StatusOK, list)
}

// View is GET /backups/{backup}.
func (h *Handlers) View(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	r, err := h.find(c, p)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, h.item(r))
}

type startBody struct {
	Name    field.String `json:"name"`
	Locked  field.Bool   `json:"is_locked"`
	Ignored field.String `json:"ignored"`
}

// Start is POST /backups. Counting the backups, the throttle and the insert happen in one transaction with the backups locked, so two
// requests at once cannot both see room (plan 10.1 d). If the node cannot be told, the row is taken back.
func (h *Handlers) Start(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body startBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Type("name", body.Name.State, validate.KindString, true)
	v.Type("is_locked", body.Locked.State, validate.KindBoolean, true)
	v.Type("ignored", body.Ignored.State, validate.KindString, true)
	if err := v.Err(); err != nil {
		return err
	}
	now := h.d.Now().In(h.d.Location)
	name, err := backups.Name(body.Name.V, now)
	if err != nil {
		w := validate.New()
		w.Custom("name", "invalid", sentence(err))
		return w.Err()
	}
	ignored, err := backups.ParseIgnored(body.Ignored.V)
	if err != nil {
		w := validate.New()
		w.Custom("ignored", "invalid", sentence(err))
		return w.Err()
	}
	// Only someone who may delete backups can make one that cannot be deleted: otherwise a user could fill the server with backups
	// that nobody can remove.
	locked := body.Locked.State == field.Valid && body.Locked.V && permissions.Has(p.Permissions, permissions.BackupDelete)

	rec, err := h.svc.Start(c.Request().Context(), p.Server.ID, StartParams{Name: name, Ignored: ignored, Locked: locked})
	var throttle *backups.ThrottleError
	var tooMany *backups.TooManyError
	switch {
	case errors.As(err, &throttle):
		return &apierr.Error{Status: http.StatusTooManyRequests, Code: apierr.CodeThrottled, Detail: throttle.Error(), RetryAfter: time.Duration(throttle.RetryAfterSeconds()) * time.Second}
	case errors.As(err, &tooMany):
		return apierr.Display(tooMany.Error())
	case errors.Is(err, controlapi.ErrNotFound):
		return apierr.NotFound()
	case err != nil:
		var we *wings.Error
		if errors.As(err, &we) {
			return controlapi.DaemonError(err)
		}
		return storeError(err)
	}
	h.log(c, p, activity.ServerBackupStart, rec, map[string]any{"name": rec.Name, "locked": rec.Locked})
	return c.JSON(http.StatusOK, h.item(rec))
}

// sentence makes the text of a rule into a sentence for the person: capital letter, full stop.
func sentence(err error) string {
	s := strings.TrimSpace(err.Error())
	if s == "" {
		return "The value is invalid."
	}
	r, size := utf8.DecodeRuneInString(s)
	s = strings.ToUpper(string(r)) + s[size:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// ToggleLock is POST /backups/{backup}/lock. Locking and unlocking need the right to delete backups.
func (h *Handlers) ToggleLock(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	r, err := h.find(c, p)
	if err != nil {
		return err
	}
	r.Locked = !r.Locked
	if err := h.d.Store.SetLocked(c.Request().Context(), p.Server.ID, r.ID, r.Locked); err != nil {
		return storeError(err)
	}
	event := activity.ServerBackupUnlock
	if r.Locked {
		event = activity.ServerBackupLock
	}
	h.log(c, p, event, r, map[string]any{"name": r.Name})
	return c.JSON(http.StatusOK, h.item(r))
}

// Download is GET /backups/{backup}/download: the address on the node and a token for 15 minutes. Only a finished backup can be
// downloaded.
func (h *Handlers) Download(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	r, err := h.find(c, p)
	if err != nil {
		return err
	}
	if err := r.State().CanDownload(); err != nil {
		return apierr.BadRequest(err.Error())
	}
	if r.Disk == backups.DiskS3 && h.d.Archives != nil {
		// The archive is on S3: the address is signed here, good for five minutes, and the browser goes to the bucket itself.
		url, err := h.d.Archives.DownloadURL(p.Server.UUID, r.UUID, DownloadValidity)
		if err != nil {
			return apierr.Internal(fmt.Errorf("sign the address of backup %s: %w", r.UUID, err))
		}
		h.log(c, p, activity.ServerBackupDownload, r, map[string]any{"name": r.Name})
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(http.StatusOK, map[string]any{"object": "signed_url", "attributes": map[string]string{"url": url}})
	}
	if r.Disk != backups.DiskWings {
		return apierr.BadRequest(errUnsupportedDisk.Error())
	}
	ctx := c.Request().Context()
	target, err := h.d.Targets.For(ctx, p.Server.ID)
	if err != nil {
		if errors.Is(err, controlapi.ErrNotFound) {
			return apierr.NotFound()
		}
		return apierr.Internal(err)
	}
	token, err := h.d.Tokens.BackupDownload(target.Node, subject{id: p.User.ID, uuid: p.User.UUID, held: p.Permissions}, target.UUID, r.UUID)
	switch {
	case errors.Is(err, wings.ErrMissingPermission):
		return apierr.Forbidden("This action is unauthorized.")
	case err != nil:
		return apierr.Internal(err)
	}
	h.log(c, p, activity.ServerBackupDownload, r, map[string]any{"name": r.Name})
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{"object": "signed_url", "attributes": map[string]string{"url": target.Node.BackupDownloadURL(token)}})
}

type restoreBody struct {
	Truncate field.Bool `json:"truncate"`
}

// Restore is POST /backups/{backup}/restore: the node unpacks the backup over the files of the server, after emptying them with
// truncate. The server is marked as restoring first, so that nothing else is done to it meanwhile; if the node cannot be told, the
// mark is taken back.
func (h *Handlers) Restore(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	// Three a quarter of an hour for a server, whoever asks. Only calls that passed the authorizer count.
	if allowed, retry := h.d.RestoreLimiter.Allow(p.Server.UUID); !allowed {
		return apierr.TooManyRequests(retry)
	}
	var body restoreBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("truncate", body.Truncate.State)
	v.Type("truncate", body.Truncate.State, validate.KindBoolean, false)
	if err := v.Err(); err != nil {
		return err
	}
	r, err := h.find(c, p)
	if err != nil {
		return err
	}
	if err := r.State().CanRestore(p.Server.State.Status); err != nil {
		return apierr.BadRequest(err.Error())
	}
	var downloadURL string
	switch {
	case r.Disk == backups.DiskWings:
	case r.Disk == backups.DiskS3 && h.d.Archives != nil:
		// The node fetches the archive from the bucket with an address that is good for five minutes.
		u, err := h.d.Archives.DownloadURL(p.Server.UUID, r.UUID, DownloadValidity)
		if err != nil {
			return apierr.Internal(fmt.Errorf("sign the address of backup %s: %w", r.UUID, err))
		}
		downloadURL = u
	default:
		return apierr.BadRequest(errUnsupportedDisk.Error())
	}

	ctx := c.Request().Context()
	target, err := h.d.Targets.For(ctx, p.Server.ID)
	if err != nil {
		if errors.Is(err, controlapi.ErrNotFound) {
			return apierr.NotFound()
		}
		return apierr.Internal(err)
	}
	marked, err := h.d.Store.SetServerStatus(ctx, p.Server.ID, p.Server.State.Status, servers.StatusRestoringBackup)
	switch {
	case err != nil:
		return storeError(err)
	case !marked:
		return apierr.BadRequest(backups.ErrServerBusy.Error())
	}
	if err := h.d.Daemon.Restore(ctx, target.Node, target.UUID, r.UUID, r.Disk, body.Truncate.V, downloadURL); err != nil {
		if _, undoErr := h.d.Store.SetServerStatus(ctx, p.Server.ID, servers.StatusRestoringBackup, p.Server.State.Status); undoErr != nil {
			h.d.Logger.Error("the status of a server was not put back after a restore failed", "server", p.Server.ID, "error", undoErr)
		}
		return controlapi.DaemonError(fmt.Errorf("restore: %w", err))
	}
	h.log(c, p, activity.ServerBackupRestore, r, map[string]any{"name": r.Name, "truncate": body.Truncate.V})
	return c.NoContent(http.StatusNoContent)
}

// Delete is DELETE /backups/{backup}: the archive on the node, then the row. A node that does not know the archive is no hindrance.
// A locked backup that finished cannot be deleted; a failed one can.
func (h *Handlers) Delete(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	r, err := h.find(c, p)
	if err != nil {
		return err
	}
	if err := r.State().CanDelete(); err != nil {
		return apierr.Display(err.Error())
	}
	ctx := c.Request().Context()
	if r.Disk == backups.DiskS3 && h.d.Archives != nil {
		// The archive goes from the bucket first, then the row: if the bucket refuses, the backup is still listed and can be deleted again.
		if err := h.d.Archives.Delete(ctx, p.Server.UUID, r.UUID); err != nil {
			return apierr.Internal(fmt.Errorf("delete the archive of backup %s: %w", r.UUID, err))
		}
		if err := h.d.Store.Delete(ctx, p.Server.ID, r.ID); err != nil {
			return storeError(err)
		}
		h.log(c, p, activity.ServerBackupDelete, r, map[string]any{"name": r.Name, "failed": !r.Successful})
		return c.NoContent(http.StatusNoContent)
	}
	if r.Disk != backups.DiskWings {
		return apierr.BadRequest(errUnsupportedDisk.Error())
	}

	target, err := h.d.Targets.For(ctx, p.Server.ID)
	if err != nil {
		if errors.Is(err, controlapi.ErrNotFound) {
			return apierr.NotFound()
		}
		return apierr.Internal(err)
	}
	if err := h.d.Daemon.Delete(ctx, target.Node, target.UUID, r.UUID); err != nil {
		var we *wings.Error
		if !errors.As(err, &we) || we.NodeStatus != http.StatusNotFound {
			return controlapi.DaemonError(err)
		}
	}
	if err := h.d.Store.Delete(ctx, p.Server.ID, r.ID); err != nil {
		return storeError(err)
	}
	h.log(c, p, activity.ServerBackupDelete, r, map[string]any{"name": r.Name, "failed": !r.Successful})
	return c.NoContent(http.StatusNoContent)
}
