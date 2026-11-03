package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Helles01/raptor-panel/internal/api/appnodes"
	"github.com/Helles01/raptor-panel/internal/api/appservers"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/outbox"
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
