package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/appnodes"
	"github.com/Helles01/raptor-panel/internal/api/appservers"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/remoteapi"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/s3"
)

// reinstaller tells the node of a server to run its install script again (settingsapi.Reinstaller).
type reinstaller struct {
	targets controlapi.Targets
	daemon  appservers.Daemon
}

func (r reinstaller) Reinstall(ctx context.Context, serverID uint64, serverUUID string) error {
	t, err := r.targets.For(ctx, serverID)
	if err != nil {
		return fmt.Errorf("find the node of server %d: %w", serverID, err)
	}
	return r.daemon.Reinstall(ctx, t.Node, serverUUID)
}

// oldNodes deletes a server on the node it was on before it was moved (remoteapi.OldNodes). The node is read as it is stored now.
type oldNodes struct {
	nodes  appnodes.Store
	daemon appservers.Daemon
}

func (o oldNodes) DeleteServer(ctx context.Context, nodeID uint64, serverUUID string) error {
	n, err := o.nodes.Connection(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("find node %d: %w", nodeID, err)
	}
	return o.daemon.Delete(ctx, n, serverUUID)
}

// settingsSender sends mail through the mail server the settings name at the moment of sending, so that a change of the settings needs no restart
// (outbox.Sender). A panel with no mail server set up sends nothing, and the job is set aside; one whose settings are wrong is tried again, since
// somebody may put them right.
type settingsSender struct {
	settings mail.Settings
	hello    string
}

func (s settingsSender) Send(ctx context.Context, m mail.Message) error {
	values, password, err := s.settings.Mail(ctx)
	if err != nil {
		return fmt.Errorf("read the mail settings: %w", err)
	}
	sender, err := mail.SenderFromSettings(func(key string) string { return values[key] }, password, s.hello)
	if errors.Is(err, mail.ErrNotConfigured) {
		return outbox.Permanent(err)
	}
	if err != nil {
		return err
	}
	return sender.Send(ctx, m)
}

// s3Backups is the S3 side of the backups: where an archive lives in the bucket, the addresses a node sends the parts of one to, and the removal. It is the
// Archives of backupapi and the Uploads of remoteapi. The key of an archive is the reference's: the server's UUID, a slash, the backup's UUID and
// ".tar.gz", so that the backups an old panel put in the bucket are found where they were put.
type s3Backups struct {
	client   *s3.Client
	partSize int64
	validity time.Duration
}

func (a s3Backups) key(serverUUID, backupUUID string) string {
	return serverUUID + "/" + backupUUID + ".tar.gz"
}

func (a s3Backups) DownloadURL(serverUUID, backupUUID string, valid time.Duration) (string, error) {
	return a.client.PresignGet(a.key(serverUUID, backupUUID), valid)
}

func (a s3Backups) Delete(ctx context.Context, serverUUID, backupUUID string) error {
	return a.client.DeleteObject(ctx, a.key(serverUUID, backupUUID))
}

// Begin starts the upload and signs one address for each part: as many parts as the size needs at the part size that is set, at least one.
func (a s3Backups) Begin(ctx context.Context, serverUUID, backupUUID string, size int64) (remoteapi.Upload, error) {
	parts := (size + a.partSize - 1) / a.partSize
	if parts < 1 {
		parts = 1
	}
	if parts > s3.MaxParts {
		return remoteapi.Upload{}, remoteapi.ErrTooLarge
	}
	key := a.key(serverUUID, backupUUID)
	id, err := a.client.CreateMultipartUpload(ctx, key)
	if err != nil {
		return remoteapi.Upload{}, err
	}
	urls := make([]string, 0, parts)
	for n := int64(1); n <= parts; n++ {
		u, err := a.client.PresignUploadPart(key, id, int(n), a.validity)
		if err != nil {
			// An upload that nobody can send to is not left open in the bucket.
			if aerr := a.client.AbortMultipartUpload(ctx, key, id); aerr != nil && !s3.IsCode(aerr, "NoSuchUpload") {
				err = errors.Join(err, aerr)
			}
			return remoteapi.Upload{}, err
		}
		urls = append(urls, u)
	}
	return remoteapi.Upload{ID: id, PartURLs: urls, PartSize: a.partSize}, nil
}

// Finish puts the parts together. A node that does not say which parts it sent is asked of the bucket. An upload the bucket no longer has may be one that
// an earlier report finished and whose answer was lost, so the object is looked at before that is called a failure.
func (a s3Backups) Finish(ctx context.Context, serverUUID, backupUUID, uploadID string, parts []remoteapi.UploadedPart, size int64) error {
	key := a.key(serverUUID, backupUUID)
	var list []s3.Part
	for _, p := range parts {
		list = append(list, s3.Part{Number: p.Number, ETag: p.ETag})
	}
	if len(list) == 0 {
		var err error
		if list, err = a.client.ListParts(ctx, key, uploadID); err != nil {
			return a.alreadyDone(ctx, key, size, err)
		}
	}
	return a.alreadyDone(ctx, key, size, a.client.CompleteMultipartUpload(ctx, key, uploadID, list))
}

// alreadyDone turns the refusal "no such upload" into success when the object is there with the size the node reported.
func (a s3Backups) alreadyDone(ctx context.Context, key string, size int64, err error) error {
	if err == nil || !s3.IsCode(err, "NoSuchUpload") {
		return err
	}
	if n, found, herr := a.client.Head(ctx, key); herr == nil && found && (size <= 0 || n == size) {
		return nil
	}
	return err
}

func (a s3Backups) Abort(ctx context.Context, serverUUID, backupUUID, uploadID string) error {
	err := a.client.AbortMultipartUpload(ctx, a.key(serverUUID, backupUUID), uploadID)
	if s3.IsCode(err, "NoSuchUpload") {
		return nil
	}
	return err
}
