package remoteapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/backups"
)

// ownBackup reads the {backup} of the route and finds it among the backups of the node's servers. What cannot be a backup is not
// looked up; one that is not the node's, or does not exist, is the same refusal.
func (h *Handlers) ownBackup(c echo.Context, nodeID uint64) (NodeBackup, error) {
	uuid := strings.ToLower(c.Param("backup"))
	if !uuidPattern.MatchString(uuid) {
		return NodeBackup{}, apierr.NotFound()
	}
	b, err := h.d.Backups.OnNode(c.Request().Context(), nodeID, uuid)
	if err != nil {
		return NodeBackup{}, found(err)
	}
	if b.NodeID != nodeID {
		return NodeBackup{}, forbidden()
	}
	return b, nil
}

// BackupUpload is GET /backups/{backup}?size=: the addresses a node uploads a backup to when it is stored on S3. Only backups kept on
// the node are handled (C-060), so this is always the reference's answer for a panel that is not set up for S3.
func (h *Handlers) BackupUpload(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	if size, err := strconv.ParseInt(c.QueryParam("size"), 10, 64); err != nil || size <= 0 {
		return apierr.BadRequest(`A non-empty "size" query parameter must be provided.`)
	}
	b, err := h.ownBackup(c, node.NodeID)
	if err != nil {
		return err
	}
	if b.Completed {
		return apierr.Conflict("This backup is already in a completed state.")
	}
	return apierr.BadRequest("The configured backup adapter is not an S3 compatible adapter.")
}

type backupReport struct {
	Successful   field.Bool   `json:"successful"`
	Checksum     field.String `json:"checksum"`
	ChecksumType field.String `json:"checksum_type"`
	Size         field.Int    `json:"size"`
}

// BackupReport is POST /backups/{backup}: a node says how a backup ended. Once it has ended it cannot be reported on again, so a node
// cannot turn a failed backup into a successful one with any checksum (C-026). The entries are written in the same step.
func (h *Handlers) BackupReport(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	var body backupReport
	if err := validate.DecodeJSON(c.Request().Body, &body, maxSmallBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("successful", body.Successful.State)
	v.Type("successful", body.Successful.State, validate.KindBoolean, false)
	v.Type("checksum", body.Checksum.State, validate.KindString, true)
	v.Type("checksum_type", body.ChecksumType.State, validate.KindString, true)
	v.Type("size", body.Size.State, validate.KindNumber, true)
	if err := v.Err(); err != nil {
		return err
	}
	b, err := h.ownBackup(c, node.NodeID)
	if err != nil {
		return err
	}

	result, err := backups.Complete(
		backups.State{Completed: b.Completed, Successful: b.Successful, Locked: b.Locked},
		backups.Report{Successful: body.Successful.V, ChecksumType: body.ChecksumType.V, Checksum: body.Checksum.V, Size: body.Size.V},
	)
	switch {
	case err == nil:
	case errors.Is(err, backups.ErrAlreadyCompleted):
		return apierr.BadRequest(err.Error())
	default:
		w := validate.New()
		w.Custom(reportField(err), "invalid", "The "+strings.ReplaceAll(err.Error(), "_", " ")+".")
		return w.Err()
	}

	stored, err := h.d.Backups.Complete(c.Request().Context(), b.ID, result, h.d.Now())
	switch {
	case err != nil:
		return apierr.Internal(fmt.Errorf("store backup: %w", err))
	case !stored:
		return apierr.BadRequest(backups.ErrAlreadyCompleted.Error())
	}

	event := activity.ServerBackupFail
	if result.Successful {
		event = activity.ServerBackupComplete
	}
	if props, ok := h.properties(event, map[string]any{"name": b.Name}); ok {
		h.logEvent(c, Event{Name: event, ServerSubject: b.ServerID, BackupSubject: b.ID, IP: c.RealIP(), Properties: props})
	}
	return noContent(c)
}

// reportField names the field of a report that a rule of backups.Complete refused: the rule's text starts with it.
func reportField(err error) string {
	for _, name := range []string{"checksum_type", "checksum", "size"} {
		if strings.HasPrefix(err.Error(), name) {
			return name
		}
	}
	return "successful"
}

type restoreReport struct {
	Successful field.Bool `json:"successful"`
}

// BackupRestoreReport is POST /backups/{backup}/restore: a node says a restore ended. The server is put back to its normal state, but
// only if it was restoring: a node cannot change the state of a server that is doing something else.
func (h *Handlers) BackupRestoreReport(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	var body restoreReport
	if err := validate.DecodeJSON(c.Request().Body, &body, maxSmallBody); err != nil {
		return err
	}
	v := validate.New()
	v.Type("successful", body.Successful.State, validate.KindBoolean, true)
	if err := v.Err(); err != nil {
		return err
	}
	b, err := h.ownBackup(c, node.NodeID)
	if err != nil {
		return err
	}

	ended, err := h.d.Backups.FinishRestore(c.Request().Context(), b.ServerID)
	if err != nil {
		return apierr.Internal(fmt.Errorf("finish restore: %w", err))
	}
	if !ended {
		h.d.Logger.Warn("a node reported the end of a restore on a server that was not restoring", "server", b.ServerID, "node", node.NodeID)
		return c.NoContent(http.StatusNoContent)
	}
	event := activity.ServerBackupRestoreFailed
	if body.Successful.State == field.Valid && body.Successful.V {
		event = activity.ServerBackupRestoreComplete
	}
	if props, ok := h.properties(event, map[string]any{"name": b.Name}); ok {
		h.logEvent(c, Event{Name: event, ServerSubject: b.ServerID, BackupSubject: b.ID, IP: c.RealIP(), Properties: props})
	}
	return c.NoContent(http.StatusNoContent)
}
