package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
)

// The server keeps db.ListenerConns pool connections for LISTEN for its whole life. With
// them taken and every upload slot busy (uploads hold a connection while the client sends
// its body, however slowly), sign-in and every other request must still get a connection.
func TestUploadSlotsLeaveRoomBesideTheListeners(t *testing.T) {
	base := migratedPool(t)
	cfg := base.Config()
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	q := db.New(pool)

	for i := 0; i < db.ListenerConns; i++ {
		c, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatalf("listener %d: %v", i, err)
		}
		defer c.Release()
	}
	for i := 0; ; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c, err := q.AcquireConnection(ctx)
		cancel()
		if errors.Is(err, db.ErrUploadConnectionsBusy) {
			break
		}
		if err != nil {
			t.Fatalf("upload %d waited for a connection instead of being refused: %v", i, err)
		}
		defer c.Release()
		if i > 16 {
			t.Fatal("upload slots are not bounded")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("no connection left for an ordinary request: %v", err)
	}
	c.Release()
}
