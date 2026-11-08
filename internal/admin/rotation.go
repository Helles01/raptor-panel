package admin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/Helles01/raptor-panel/internal/nodes"
)

// RotationStore is the part of the storage that replaces a secret.
type RotationStore interface {
	// NodeConfig gathers what the configuration file of a node needs, apart from its token, or answers ErrNotFound.
	NodeConfig(ctx context.Context, id uint64) (nodes.WingsNode, error)
	// ReplaceNodeToken stores new credentials for a node: the token id and the sealed token change in one transaction, so there is no moment
	// at which the old token is valid and the new one is not yet. ErrNotFound for a node that is not there.
	ReplaceNodeToken(ctx context.Context, id uint64, creds nodes.Credentials) error
}

// Rotation replaces secrets that must be assumed known to someone else (plan 13.3).
type Rotation struct {
	store  RotationStore
	random io.Reader
	appURL string
}

// NewRotation returns a Rotation. appURL is the address of the panel, which goes into the configuration of a node. random defaults to
// crypto/rand.Reader, as for Service.
func NewRotation(store RotationStore, appURL string, random io.Reader) (*Rotation, error) {
	if store == nil || appURL == "" {
		return nil, errors.New("admin: a rotation needs a store and the address of the panel")
	}
	if random == nil {
		random = rand.Reader
	}
	return &Rotation{store: store, random: random, appURL: appURL}, nil
}

// RotatedNode is a node with a new token, and the configuration its Wings needs from now on. The file holds the token, so it is shown here
// once and kept nowhere else.
type RotatedNode struct {
	ID     uint64
	UUID   string
	Config []byte
}

// RotateNodeToken gives a node a new token. The old one stops working at once, and the panel cannot reach the node until its config.yml is
// replaced and Wings restarted; the servers on the node keep running meanwhile.
func (r *Rotation) RotateNodeToken(ctx context.Context, id uint64) (RotatedNode, error) {
	node, err := r.store.NodeConfig(ctx, id)
	if err != nil {
		return RotatedNode{}, err
	}
	creds, err := nodes.NewCredentials(r.random)
	if err != nil {
		return RotatedNode{}, err
	}
	// The file is made before anything is stored: a node whose file cannot be made keeps the token it has.
	config, err := nodes.WingsConfig(node, creds, r.appURL)
	if err != nil {
		return RotatedNode{}, fmt.Errorf("render the configuration of node %d: %w", id, err)
	}
	if err := r.store.ReplaceNodeToken(ctx, id, creds); err != nil {
		return RotatedNode{}, err
	}
	return RotatedNode{ID: id, UUID: node.UUID, Config: config}, nil
}
