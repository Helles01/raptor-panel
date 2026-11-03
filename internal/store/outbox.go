package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Helles01/raptor-panel/internal/db/dbq"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// OutboxQueue takes jobs.
func (d *DB) OutboxQueue() outbox.Queue { return outboxQueue{d} }

// OutboxStore is the store of the runner of the outbox.
func (d *DB) OutboxStore() outbox.Store { return outboxStore{d} }

// OutboxNodes finds how to reach a node, with its token opened.
func (d *DB) OutboxNodes() outbox.Nodes { return outboxNodes{d} }

type (
	outboxQueue struct{ *DB }
	outboxStore struct{ *DB }
	outboxNodes struct{ *DB }
)

func (s outboxQueue) Enqueue(ctx context.Context, j outbox.New) error {
	if err := s.q.EnqueueJob(ctx, dbq.EnqueueJobParams{Kind: j.Kind, Payload: string(j.Payload), RunAt: s.now().UTC()}); err != nil {
		return fmt.Errorf("queue a %s job: %w", j.Kind, err)
	}
	return nil
}

// Claim takes the jobs that are due and leases them. The select skips rows another transaction has locked, so two processes never take the same
// job; a job whose lease has passed (the process that took it died) is due again.
func (s outboxStore) Claim(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]outbox.Job, error) {
	var jobs []outbox.Job
	err := s.inTx(ctx, func(q *dbq.Queries) error {
		rows, err := q.DueJobs(ctx, dbq.DueJobsParams{RunAt: now.UTC(), LeaseUntil: sql.NullTime{Time: now.UTC(), Valid: true}, Limit: int32(limit)})
		if err != nil {
			return fmt.Errorf("find the jobs that are due: %w", err)
		}
		until := sql.NullTime{Time: now.Add(lease).UTC(), Valid: true}
		for _, r := range rows {
			if err := q.LeaseJob(ctx, dbq.LeaseJobParams{LeaseUntil: until, ID: r.ID}); err != nil {
				return fmt.Errorf("lease job %d: %w", r.ID, err)
			}
			jobs = append(jobs, outbox.Job{ID: r.ID, Kind: r.Kind, Payload: []byte(r.Payload), Attempts: int(r.Attempts)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s outboxStore) Done(ctx context.Context, id uint64) error {
	if err := s.q.DeleteJob(ctx, id); err != nil {
		return fmt.Errorf("remove job %d: %w", id, err)
	}
	return nil
}

func (s outboxStore) Retry(ctx context.Context, id uint64, attempts int, at time.Time, failure string) error {
	err := s.q.RetryJob(ctx, dbq.RetryJobParams{Attempts: uint32(attempts), RunAt: at.UTC(), Failure: sql.NullString{String: failure, Valid: true}, ID: id})
	if err != nil {
		return fmt.Errorf("put job %d back: %w", id, err)
	}
	return nil
}

func (s outboxStore) Fail(ctx context.Context, id uint64, attempts int, failure string) error {
	err := s.q.FailJob(ctx, dbq.FailJobParams{Attempts: uint32(attempts), Failure: sql.NullString{String: failure, Valid: true}, ID: id})
	if err != nil {
		return fmt.Errorf("set job %d aside: %w", id, err)
	}
	return nil
}

func (s outboxNodes) Node(ctx context.Context, id uint64) (wings.Node, error) {
	row, err := s.q.NodeForDaemon(ctx, uint32(id))
	switch {
	case errors.Is(err, sql.ErrNoRows) || id > 1<<32-1:
		return wings.Node{}, outbox.ErrNoNode
	case err != nil:
		return wings.Node{}, fmt.Errorf("find node %d: %w", id, err)
	}
	token, ok, err := s.secret(ctx, "nodes", id, "daemon_token")
	if err != nil {
		return wings.Node{}, err
	}
	if !ok {
		return wings.Node{}, outbox.ErrNoNode
	}
	return wings.Node{Scheme: row.Scheme, FQDN: row.Fqdn, Port: int(row.Daemonlisten), Token: token}, nil
}
