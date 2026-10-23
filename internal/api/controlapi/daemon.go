package controlapi

import (
	"context"

	"github.com/Helles01/raptor-panel/internal/wings"
)

// wingsDaemon is the Daemon that talks to real nodes.
type wingsDaemon struct{ factory *wings.Factory }

// NewDaemon returns the Daemon that calls nodes through the factory. A node whose address or token is not fit for use is an error
// of the call, not a panic.
func NewDaemon(f *wings.Factory) Daemon { return wingsDaemon{factory: f} }

func (d wingsDaemon) Power(ctx context.Context, n wings.Node, serverUUID, action string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.Power(ctx, serverUUID, action)
}

func (d wingsDaemon) Command(ctx context.Context, n wings.Node, serverUUID, command string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.Command(ctx, serverUUID, command)
}

func (d wingsDaemon) Details(ctx context.Context, n wings.Node, serverUUID string) (wings.Details, error) {
	c, err := d.factory.Client(n)
	if err != nil {
		return wings.Details{}, err
	}
	return c.Details(ctx, serverUUID)
}
