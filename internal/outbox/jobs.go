package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// The kinds of job.
const (
	KindMail        = "mail.send"
	KindDeauthorize = "node.deauthorize"
)

// New is a job to store: its kind and its payload as JSON. The store of a change writes it in the transaction of the change.
type New struct {
	Kind    string
	Payload []byte
}

// Queue takes jobs. It is the part of a store that a handler needs to put a job there outside a transaction of its own.
type Queue interface {
	Enqueue(ctx context.Context, j New) error
}

// MailJob is the job of sending a message.
func MailJob(m mail.Message) (New, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return New{}, fmt.Errorf("encode mail: %w", err)
	}
	return New{Kind: KindMail, Payload: b}, nil
}

// Mailer puts mail in a queue. It is the Outbox the endpoint packages take.
type Mailer struct{ Queue Queue }

// Send queues a message.
func (m Mailer) Send(ctx context.Context, msg mail.Message) error {
	j, err := MailJob(msg)
	if err != nil {
		return err
	}
	return m.Queue.Enqueue(ctx, j)
}

// Sender sends a message over the network. *mail.Sender is one.
type Sender interface {
	Send(ctx context.Context, m mail.Message) error
}

// PermanentError is implemented by the errors of a sender that know trying again is no use.
type PermanentError interface{ Permanent() bool }

// MailHandler sends the message of a job. A message that cannot be read, or that the server refuses for good, is set aside; a server
// that is not there, or says to try later, is tried again.
func MailHandler(s Sender) Handler {
	return func(ctx context.Context, job Job) error {
		var m mail.Message
		if err := json.Unmarshal(job.Payload, &m); err != nil {
			return Permanent(fmt.Errorf("read mail job: %w", err))
		}
		err := s.Send(ctx, m)
		var pe PermanentError
		if errors.As(err, &pe) && pe.Permanent() {
			return Permanent(err)
		}
		return err
	}
}

// Deauthorization is the payload of a job that tells a node a user lost access: their websocket and SFTP sessions end and their signed
// tokens stop working, on the servers named, or on all of them when none is.
type Deauthorization struct {
	NodeID      uint64   `json:"node_id"`
	UserUUID    string   `json:"user_uuid"`
	ServerUUIDs []string `json:"server_uuids,omitempty"`
}

// DeauthorizeJob is the job of telling a node.
func DeauthorizeJob(d Deauthorization) (New, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return New{}, fmt.Errorf("encode deauthorization: %w", err)
	}
	return New{Kind: KindDeauthorize, Payload: b}, nil
}

// Nodes finds how to reach a node.
type Nodes interface {
	// Node returns the node with its token opened, or ErrNoNode.
	Node(ctx context.Context, id uint64) (wings.Node, error)
}

// ErrNoNode is what Nodes answers for a node that is gone: there is nobody left to tell.
var ErrNoNode = errors.New("outbox: no such node")

// Deauthorizer tells a node a user lost access. NewDeauthorizer is the one that talks to a real Wings.
type Deauthorizer interface {
	Deauthorize(ctx context.Context, n wings.Node, userUUID string, serverUUIDs ...string) error
}

// NewDeauthorizer returns the Deauthorizer that calls nodes through the factory.
func NewDeauthorizer(f *wings.Factory) Deauthorizer { return wingsDeauthorizer{f} }

type wingsDeauthorizer struct{ f *wings.Factory }

func (w wingsDeauthorizer) Deauthorize(ctx context.Context, n wings.Node, userUUID string, serverUUIDs ...string) error {
	c, err := w.f.Client(n)
	if err != nil {
		return err
	}
	return c.Deauthorize(ctx, userUUID, serverUUIDs...)
}

// DeauthorizeHandler tells the node of a job. A node that is gone needs no telling. A node that refuses the request (a 4xx, other than
// "try later") will refuse it again, so the job is set aside; a node that cannot be reached, or fails, is tried again.
func DeauthorizeHandler(nodes Nodes, d Deauthorizer) Handler {
	return func(ctx context.Context, job Job) error {
		var p Deauthorization
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return Permanent(fmt.Errorf("read deauthorization job: %w", err))
		}
		n, err := nodes.Node(ctx, p.NodeID)
		switch {
		case errors.Is(err, ErrNoNode):
			return nil
		case err != nil:
			return fmt.Errorf("find node %d: %w", p.NodeID, err)
		}
		err = d.Deauthorize(ctx, n, p.UserUUID, p.ServerUUIDs...)
		var we *wings.Error
		if errors.As(err, &we) && we.NodeStatus >= 400 && we.NodeStatus < 500 && we.NodeStatus != http.StatusRequestTimeout && we.NodeStatus != http.StatusTooManyRequests {
			return Permanent(err)
		}
		return err
	}
}
