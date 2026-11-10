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
	capacity := uploadSlots(pool)
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

// uploadSlots is how many connections uploads may hold at once: two spare for ordinary
// requests, on top of the listeners' connections — those never come back to the pool,
// and counting them as available let uploads take the last one.
func uploadSlots(pool *pgxpool.Pool) int {
	return max(1, int(pool.Config().MaxConns)-ListenerConns-2)
}

// UploadSlots reports the number of upload slots of this pool (1 without a pool).
func (q *Queries) UploadSlots() int {
	if pool, ok := q.db.(*pgxpool.Pool); ok {
		return uploadSlots(pool)
	}
	return 1
}
