// Package library keeps media indexes (music, e-books) in step with the change log:
// every change to a file is a change_log row, so a library processes only rows after
// its per-user cursor — work in proportion to what changed, none when idle.
package library

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/coalesce"
	"discodrive/internal/db"
)

type Library interface {
	Name() string
	State(ctx context.Context, userID pgtype.UUID) (folder string, cursor int64, ok bool, err error)
	SetCursor(ctx context.Context, userID pgtype.UUID, seq int64) error
	Accepts(diskPath string) bool
	Index(ctx context.Context, userID, nodeID, diskPath string) error
	Remove(ctx context.Context, nodeID string) error
	IndexUnder(ctx context.Context, userID, dirNodeID string) error
	RemoveUnder(ctx context.Context, userID pgtype.UUID, dirPath string) error
}

const pageSize = 500

// CatchUp processes the user's changes after the library's cursor.
func CatchUp(ctx context.Context, q *db.Queries, lib Library, userID pgtype.UUID) error {
	folder, cursor, ok, err := lib.State(ctx, userID)
	if err != nil || !ok {
		return err
	}
	uid := db.UUIDString(userID)
	for {
		rows, err := q.ListChangesAfter(ctx, db.ListChangesAfterParams{UserID: userID, Seq: cursor, Limit: pageSize})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		seen := make(map[pgtype.UUID]bool, len(rows))
		for _, c := range rows {
			if seen[c.NodeID] {
				continue
			}
			seen[c.NodeID] = true
			nid, p := db.UUIDString(c.NodeID), c.DiskPath.String
			// The library folder belongs to the library too; its own deletion is what
			// drops everything under it (the first case below).
			inside := c.DiskPath.Valid && (p == folder || strings.HasPrefix(p, folder+"/"))
			if c.IsDir && c.Deleted && p == folder {
				inside = false
			}
			var err error
			switch {
			case c.IsDir && (c.Deleted || !inside):
				// The log holds only the folder, not what is under it.
				if c.DiskPath.Valid {
					err = lib.RemoveUnder(ctx, userID, p)
				}
			case c.IsDir:
				err = lib.IndexUnder(ctx, uid, nid) // a restored or moved-in folder
			case !c.Deleted && inside && lib.Accepts(p):
				err = lib.Index(ctx, uid, nid, p)
			default:
				err = lib.Remove(ctx, nid) // trashed, moved out, or not a media file
			}
			if err != nil {
				log.Printf("discodrive: %s %s: %v", lib.Name(), p, err)
			}
		}
		cursor = rows[len(rows)-1].Seq
		if err := lib.SetCursor(ctx, userID, cursor); err != nil {
			return err
		}
		if len(rows) < pageSize {
			return nil
		}
	}
}

// Tracker runs CatchUp for users whose change log moved, and for everyone at start and
// after every reconnect of its LISTEN connection.
type Tracker struct {
	pool     *pgxpool.Pool
	q        *db.Queries
	libs     []Library
	debounce time.Duration
	mu       sync.Mutex
	timers   map[string]*time.Timer
	gate     coalesce.Gate
}

func NewTracker(pool *pgxpool.Pool, libs ...Library) *Tracker {
	return &Tracker{pool: pool, q: db.New(pool), libs: libs, debounce: time.Second, timers: map[string]*time.Timer{}}
}

func (t *Tracker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := t.listen(ctx); err != nil && ctx.Err() == nil {
			log.Printf("discodrive: library LISTEN: %v (reconnecting in 1s)", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}
}

func (t *Tracker) listen(ctx context.Context) error {
	conn, err := t.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN change_log"); err != nil {
		return err
	}
	t.catchUpAll(ctx) // at start and after every reconnect: nothing logged meanwhile is lost
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if i := strings.LastIndexByte(n.Payload, ':'); i > 0 {
			t.schedule(ctx, n.Payload[:i])
		}
	}
}

// schedule catches the user up once their notifications have been quiet for the
// debounce period, so a burst (an album upload) is one pass.
func (t *Tracker) schedule(ctx context.Context, userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tm, ok := t.timers[userID]; ok {
		tm.Reset(t.debounce)
		return
	}
	t.timers[userID] = time.AfterFunc(t.debounce, func() {
		t.mu.Lock()
		delete(t.timers, userID)
		t.mu.Unlock()
		t.catchUpUser(ctx, userID)
	})
}

func (t *Tracker) catchUpUser(ctx context.Context, userID string) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return
	}
	for _, lib := range t.libs {
		t.gate.Run(lib.Name()+"/"+userID, func() {
			if err := CatchUp(ctx, t.q, lib, uid); err != nil && ctx.Err() == nil {
				log.Printf("discodrive: %s catch-up user=%s: %v", lib.Name(), userID, err)
			}
		})
	}
}

func (t *Tracker) catchUpAll(ctx context.Context) {
	users, err := t.q.ListUserIDs(ctx)
	if err != nil {
		log.Printf("discodrive: library catch-up: %v", err)
		return
	}
	for _, u := range users {
		t.catchUpUser(ctx, db.UUIDString(u))
	}
}
