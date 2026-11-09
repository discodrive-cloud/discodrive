package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrNoTx: the Queries were made on a handle that cannot open a transaction.
var ErrNoTx = errors.New("db: handle cannot begin a transaction")

// InTx runs fn on one transaction (a savepoint when q is already on one) and commits
// when it returns nil; any error rolls everything fn did back.
func (q *Queries) InTx(ctx context.Context, fn func(*Queries) error) error {
	b, ok := q.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return ErrNoTx
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
