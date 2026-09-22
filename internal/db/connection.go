package db

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
)

var ErrUploadConnectionsBusy = errors.New("too many active uploads; retry shortly")
var uploadConnectionSlots sync.Map

type UploadConnection struct {
	*pgxpool.Conn
	slot chan struct{}
}

func (c *UploadConnection) Release()          { c.Conn.Release(); <-c.slot }
func (c *UploadConnection) Hijack() *pgx.Conn { conn := c.Conn.Hijack(); <-c.slot; return conn }

// Keep pool capacity for authentication and ordinary requests even if upload
// clients stop sending bytes. This limit is shared by all queries for this pool.
func (q *Queries) AcquireConnection(ctx context.Context) (*UploadConnection, error) {
	pool, ok := q.db.(*pgxpool.Pool)
	if !ok {
		return nil, errors.New("database connection pool required")
	}
	capacity := max(1, int(pool.Config().MaxConns)-2)
	value, _ := uploadConnectionSlots.LoadOrStore(pool, make(chan struct{}, capacity))
	slots := value.(chan struct{})
	select {
	case slots <- struct{}{}:
	default:
		return nil, ErrUploadConnectionsBusy
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		<-slots
		return nil, err
	}
	return &UploadConnection{Conn: conn, slot: slots}, nil
}
