package fileapi

import (
	"context"

	"github.com/Helles01/raptor-panel/internal/wings"
)

// wingsDaemon is the Daemon that talks to real nodes.
type wingsDaemon struct{ factory *wings.Factory }

// NewDaemon returns the Daemon that calls nodes through the factory.
func NewDaemon(f *wings.Factory) Daemon { return wingsDaemon{factory: f} }

func (d wingsDaemon) List(ctx context.Context, n wings.Node, serverUUID, directory string) ([]wings.FileObject, error) {
	c, err := d.factory.Client(n)
	if err != nil {
		return nil, err
	}
	return c.ListDirectory(ctx, serverUUID, directory)
}

func (d wingsDaemon) Read(ctx context.Context, n wings.Node, serverUUID, path string, max int64) ([]byte, error) {
	c, err := d.factory.Client(n)
	if err != nil {
		return nil, err
	}
	return c.ReadFile(ctx, serverUUID, path, max)
}

func (d wingsDaemon) Write(ctx context.Context, n wings.Node, serverUUID, path string, content []byte) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.WriteFile(ctx, serverUUID, path, content)
}

func (d wingsDaemon) CreateDirectory(ctx context.Context, n wings.Node, serverUUID, name, path string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.CreateDirectory(ctx, serverUUID, name, path)
}

func (d wingsDaemon) Rename(ctx context.Context, n wings.Node, serverUUID, root string, items []wings.RenameItem) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.RenameFiles(ctx, serverUUID, root, items)
}

func (d wingsDaemon) Copy(ctx context.Context, n wings.Node, serverUUID, location string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.CopyFile(ctx, serverUUID, location)
}

func (d wingsDaemon) Delete(ctx context.Context, n wings.Node, serverUUID, root string, names []string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.DeleteFiles(ctx, serverUUID, root, names)
}

func (d wingsDaemon) Compress(ctx context.Context, n wings.Node, serverUUID, root string, names []string) (wings.FileObject, error) {
	c, err := d.factory.Client(n)
	if err != nil {
		return wings.FileObject{}, err
	}
	return c.CompressFiles(ctx, serverUUID, root, names)
}

func (d wingsDaemon) Decompress(ctx context.Context, n wings.Node, serverUUID, root, file string) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.DecompressFile(ctx, serverUUID, root, file)
}

func (d wingsDaemon) Chmod(ctx context.Context, n wings.Node, serverUUID, root string, items []wings.ChmodItem) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.ChmodFiles(ctx, serverUUID, root, items)
}

func (d wingsDaemon) Pull(ctx context.Context, n wings.Node, serverUUID string, o wings.PullOptions) error {
	c, err := d.factory.Client(n)
	if err != nil {
		return err
	}
	return c.PullFile(ctx, serverUUID, o)
}
