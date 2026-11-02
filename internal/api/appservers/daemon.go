package appservers

import (
	"context"

	"github.com/Helles01/raptor-panel/internal/wings"
)

// wingsDaemon is the Daemon that talks to real nodes.
type wingsDaemon struct{ factory *wings.Factory }

// NewDaemon returns the Daemon that calls nodes through the factory.
func NewDaemon(f *wings.Factory) Daemon { return wingsDaemon{factory: f} }

func (d wingsDaemon) Create(ctx context.Context, n wings.Node, serverUUID string, start bool) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.CreateServer(ctx, serverUUID, start)
}

func (d wingsDaemon) Sync(ctx context.Context, n wings.Node, serverUUID string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.Sync(ctx, serverUUID)
}

func (d wingsDaemon) Reinstall(ctx context.Context, n wings.Node, serverUUID string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.Reinstall(ctx, serverUUID)
}

func (d wingsDaemon) Delete(ctx context.Context, n wings.Node, serverUUID string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.Delete(ctx, serverUUID)
}
