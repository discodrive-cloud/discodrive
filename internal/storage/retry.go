package storage

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// ErrBusy: a write kept colliding with concurrent changes to the same part of the tree
// (deadlock, serialization failure, or a path that moved under it) and gave up after a
// few attempts. Nothing was changed; the caller may simply retry.
var ErrBusy = errors.New("the folder is being changed by another operation; retry shortly")

// errStale: a path computed before the transaction no longer matches the row it was
// read from (a folder above was renamed or moved meanwhile). The whole operation is run
// again from its reads.
var errStale = errors.New("path changed concurrently")

const retryAttempts = 3

// retryable reports whether err is worth running the operation again for: a deadlock
// or serialization failure rolled the transaction back, or a path went stale.
func retryable(err error) bool {
	if errors.Is(err, errStale) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001")
}

// withRetry runs fn (one whole transaction, reads included) up to retryAttempts times
// while it fails with a retryable error, with a short jittered pause between attempts.
// fn must leave nothing behind when it fails: its side effects are undone on rollback,
// done after its last fallible statement, or idempotent. A retryable failure on the
// last attempt becomes ErrBusy.
func withRetry(ctx context.Context, fn func() error) error {
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !retryable(err) {
			return err
		}
		if attempt == retryAttempts {
			return fmt.Errorf("%w (%v)", ErrBusy, err)
		}
		pause := time.Duration(attempt)*5*time.Millisecond + rand.N(20*time.Millisecond)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
}

// retryValue is withRetry for an operation that returns a value.
func retryValue[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var out T
	err := withRetry(ctx, func() error {
		v, err := fn()
		if err == nil {
			out = v
		}
		return err
	})
	return out, err
}

// lockParent re-checks, inside the write's transaction, that the folder a node goes
// into is still live and still at prefix (the path the caller built the child's path
// from), and holds it so until the commit. A concurrent Delete of that folder (or of any
// folder above it) either waits for this write and trashes the new node with the rest,
// or has already trashed the folder and the write fails (ErrNotFound). A concurrent
// rename or move of a folder above waits too (LockLiveSubtreeDirs), or has already
// changed the prefix: errStale, and the caller runs again with the new path. A nil
// parent is the root, whose prefix never changes.
func lockParent(ctx context.Context, qtx *db.Queries, parent pgtype.UUID, prefix string) error {
	if !parent.Valid {
		return nil
	}
	p, err := qtx.LockLiveParent(ctx, parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if p.DiskPath.String != prefix {
		return errStale
	}
	return nil
}

// lockSelf locks the node an operation changes and checks it is still live at the path
// the operation was computed from.
func lockSelf(ctx context.Context, qtx *db.Queries, id pgtype.UUID, path string) (db.Node, error) {
	n, err := qtx.GetNodeForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Node{}, ErrNotFound
	}
	if err != nil {
		return db.Node{}, err
	}
	if n.DiskPath.String != path {
		return db.Node{}, errStale
	}
	return n, nil
}
