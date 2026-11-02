package backupapi

import (
	"context"

	"github.com/Helles01/raptor-panel/internal/wings"
)

// wingsDaemon is the Daemon that talks to real nodes.
type wingsDaemon struct{ factory *wings.Factory }

// NewDaemon returns the Daemon that calls nodes through the factory.
func NewDaemon(f *wings.Factory) Daemon { return wingsDaemon{factory: f} }

func (d wingsDaemon) Create(ctx context.Context, n wings.Node, serverUUID, disk, backupUUID, ignore string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.CreateBackup(ctx, serverUUID, disk, backupUUID, ignore)
}

func (d wingsDaemon) Restore(ctx context.Context, n wings.Node, serverUUID, backupUUID, disk string, truncate bool, downloadURL string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.RestoreBackup(ctx, serverUUID, backupUUID, disk, truncate, downloadURL)
}

func (d wingsDaemon) Delete(ctx context.Context, n wings.Node, serverUUID, backupUUID string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.DeleteBackup(ctx, serverUUID, backupUUID)
}
