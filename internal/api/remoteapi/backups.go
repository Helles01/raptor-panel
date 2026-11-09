package remoteapi

import (
	"context"
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

// BackupUpload is GET /backups/{backup}?size=: the addresses a node uploads a backup to when it is stored on S3. The upload is started here and its id kept
// on the backup, which is what finishes it when the node reports. A backup that is not on S3, or a panel that has no S3 settings, gets the reference's answer
// for a panel that is not set up for S3.
func (h *Handlers) BackupUpload(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	size, err := strconv.ParseInt(c.QueryParam("size"), 10, 64)
	if err != nil || size <= 0 {
		return apierr.BadRequest(`A non-empty "size" query parameter must be provided.`)
	}
	b, err := h.ownBackup(c, node.NodeID)
	if err != nil {
		return err
	}
	if b.Completed {
		return apierr.Conflict("This backup is already in a completed state.")
	}
	if b.Disk != backups.DiskS3 || h.d.Uploads == nil {
		return apierr.BadRequest("The configured backup adapter is not an S3 compatible adapter.")
	}

	ctx := c.Request().Context()
	up, err := h.d.Uploads.Begin(ctx, b.ServerUUID, b.UUID, size)
	switch {
	case errors.Is(err, ErrTooLarge):
		return apierr.BadRequest(err.Error())
	case err != nil:
		return apierr.Internal(fmt.Errorf("start the upload of backup %s: %w", b.UUID, err))
	}
	stored, err := h.d.Backups.SetUploadID(ctx, b.ID, up.ID)
	if err != nil || !stored {
		// The upload is of no use to anybody now: throw it away, and say why the node does not get its addresses.
		if aerr := h.d.Uploads.Abort(ctx, b.ServerUUID, b.UUID, up.ID); aerr != nil {
			h.d.Logger.Warn("an upload that was not kept was not aborted", "backup", b.UUID, "error", aerr)
		}
		if err != nil {
			return apierr.Internal(fmt.Errorf("store the upload of backup %s: %w", b.UUID, err))
		}
		return apierr.Conflict("This backup is already in a completed state.")
	}
	return c.JSON(http.StatusOK, map[string]any{"parts": up.PartURLs, "part_size": up.PartSize})
}

type backupReport struct {
	Successful   field.Bool   `json:"successful"`
	Checksum     field.String `json:"checksum"`
	ChecksumType field.String `json:"checksum_type"`
	Size         field.Int    `json:"size"`
	// Parts are the pieces of an archive kept on S3, as the storage tagged them when the node sent them; absent when the node does not say.
	Parts []reportPart `json:"parts"`
}

type reportPart struct {
	ETag       field.String `json:"etag"`
	PartNumber field.Int    `json:"part_number"`
}

// maxBackupReportBody is the largest report of a backup: with the parts of an upload to S3 it can have thousands of lines.
const maxBackupReportBody = 1 << 20

// BackupReport is POST /backups/{backup}: a node says how a backup ended. Once it has ended it cannot be reported on again, so a node
// cannot turn a failed backup into a successful one with any checksum (C-026). The entries are written in the same step.
func (h *Handlers) BackupReport(c echo.Context) error {
	node, err := scope(c)
	if err != nil {
		return err
	}
	var body backupReport
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBackupReportBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("successful", body.Successful.State)
	v.Type("successful", body.Successful.State, validate.KindBoolean, false)
	v.Type("checksum", body.Checksum.State, validate.KindString, true)
	v.Type("checksum_type", body.ChecksumType.State, validate.KindString, true)
	v.Type("size", body.Size.State, validate.KindNumber, true)
	parts := checkParts(v, body.Parts)
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

	if b.Disk == backups.DiskS3 {
		if err := h.finishUpload(c.Request().Context(), b, result, parts); err != nil {
			return err
		}
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

// ErrTooLarge is what an Uploads answers for an archive that would need more parts than an upload may have.
var ErrTooLarge = errors.New("The backup is too large to be uploaded in parts of the size that is set.")

// maxReportedParts is the most parts an upload may have (the protocol's limit).
const maxReportedParts = 10000

// checkParts reads the parts a node says it sent and refuses the ones that cannot be parts. A report that names none gives nil, and the storage
// is asked which there are.
func checkParts(v *validate.Validator, in []reportPart) []UploadedPart {
	if len(in) > maxReportedParts {
		v.Custom("parts", "max", "The parts may not have more than 10000 items.")
		return nil
	}
	var out []UploadedPart
	for i, p := range in {
		name := "parts." + strconv.Itoa(i)
		tagOK := p.ETag.State == field.Valid && p.ETag.V != "" && len(p.ETag.V) <= 256
		numberOK := p.PartNumber.State == field.Valid && p.PartNumber.V >= 1 && p.PartNumber.V <= maxReportedParts
		if !tagOK {
			v.Custom(name+".etag", "invalid", "The "+name+".etag field is invalid.")
		}
		if !numberOK {
			v.Custom(name+".part_number", "invalid", "The "+name+".part_number field is invalid.")
		}
		if tagOK && numberOK {
			out = append(out, UploadedPart{Number: int(p.PartNumber.V), ETag: p.ETag.V})
		}
	}
	return out
}

// finishUpload ends the upload of a backup kept on S3 as the node says it ended: the parts are put together if it went well, and thrown away if it
// did not. It runs before the end is stored: if the storage refuses, the backup stays unfinished and the node reports again, which is better than a
// backup that is marked successful and has no archive.
func (h *Handlers) finishUpload(ctx context.Context, b NodeBackup, r backups.Result, parts []UploadedPart) error {
	if h.d.Uploads == nil {
		if !r.Successful {
			return nil
		}
		return apierr.Internal(fmt.Errorf("backup %s is kept on S3 and was reported done, but the panel has no S3 settings", b.UUID))
	}
	if b.UploadID == "" {
		// A node that failed before it asked for the addresses has no upload to end.
		if !r.Successful {
			return nil
		}
		return apierr.Display("Cannot complete backup request: no upload_id present on model.")
	}
	if !r.Successful {
		// The backup is failed whatever the storage says; a part that stays there is for the bucket's own rules to clear.
		if err := h.d.Uploads.Abort(ctx, b.ServerUUID, b.UUID, b.UploadID); err != nil {
			h.d.Logger.Warn("the upload of a failed backup was not aborted", "backup", b.UUID, "error", err)
		}
		return nil
	}
	if err := h.d.Uploads.Finish(ctx, b.ServerUUID, b.UUID, b.UploadID, parts, r.Bytes); err != nil {
		return apierr.Internal(fmt.Errorf("finish the upload of backup %s: %w", b.UUID, err))
	}
	return nil
}
