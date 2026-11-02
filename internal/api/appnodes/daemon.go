package appnodes

import (
	"context"

	"github.com/Helles01/raptor-panel/internal/wings"
)

// wingsDaemon is the Daemon that talks to real nodes.
type wingsDaemon struct{ factory *wings.Factory }

// NewDaemon returns the Daemon that calls nodes through the factory.
func NewDaemon(f *wings.Factory) Daemon { return wingsDaemon{factory: f} }

func (d wingsDaemon) UpdateConfiguration(ctx context.Context, n wings.Node, config []byte) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.UpdateConfiguration(ctx, config)
}
