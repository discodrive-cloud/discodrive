// Package rescan executes queued disk↔database reconciliations (table rescan_requests).
// Requests come from startup, the admin panel and `server rescan`. The running server
// is the only executor, so reconciliation never races uploads in another process.
package rescan

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// Enqueue queues a reconciliation of one user, or of every user when userID is invalid.
func Enqueue(ctx context.Context, q *db.Queries, userID pgtype.UUID, by string) (int64, error) {
	r, err := q.CreateRescanRequest(ctx, db.CreateRescanRequestParams{UserID: userID, RequestedBy: by})
	return r.ID, err
}

// Runner executes the queue, one request at a time.
type Runner struct {
	pool *pgxpool.Pool
	q    *db.Queries
	fs   *storage.FileService
}

func NewRunner(pool *pgxpool.Pool, fs *storage.FileService) *Runner {
	return &Runner{pool: pool, q: db.New(pool), fs: fs}
}

// Run drains the queue and then waits for new requests until ctx is cancelled. After a
// lost LISTEN connection it drains again, so nothing queued meanwhile is missed.
func (r *Runner) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := r.listen(ctx); err != nil && ctx.Err() == nil {
			log.Printf("discodrive: rescan LISTEN: %v (reconnecting in 1s)", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}
}

func (r *Runner) listen(ctx context.Context) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN rescan_requests"); err != nil {
		return err
	}
	for {
		if err := r.Drain(ctx); err != nil && ctx.Err() == nil {
			log.Printf("discodrive: rescan: %v", err)
		}
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return err
		}
	}
}

// Drain executes every pending request, oldest first. Requests for the same target that
// are pending together share one run and its outcome.
func (r *Runner) Drain(ctx context.Context) error {
	pending, err := r.q.ListPendingRescanRequests(ctx)
	if err != nil {
		return err
	}
	done := map[string]storage.ReconcileStats{}
	for _, req := range pending {
		key := db.UUIDString(req.UserID) // "" means every user
		st, ok := done[key]
		if !ok {
			if err := r.q.StartRescanRequest(ctx, req.ID); err != nil {
				return err
			}
			st = r.execute(ctx, req.UserID)
			if ctx.Err() != nil {
				return ctx.Err() // left unfinished: it runs again at the next start
			}
			done[key] = st
			log.Printf("discodrive: rescan #%d (%s, %s): imported %d, missing %d, changed %d, errors %d",
				req.ID, target(req), req.RequestedBy, st.Imported, st.Missing, st.Changed, st.Errors)
		}
		var errText pgtype.Text
		if len(st.ErrorText) > 0 {
			errText = pgtype.Text{String: strings.Join(st.ErrorText, "\n"), Valid: true}
		}
		if err := r.q.FinishRescanRequest(ctx, db.FinishRescanRequestParams{
			ID: req.ID, Imported: int32(st.Imported), Missing: int32(st.Missing),
			Changed: int32(st.Changed), Errors: int32(st.Errors), ErrorText: errText,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) execute(ctx context.Context, userID pgtype.UUID) storage.ReconcileStats {
	var total storage.ReconcileStats
	users := []pgtype.UUID{userID}
	if !userID.Valid {
		var err error
		if users, err = r.q.ListUserIDs(ctx); err != nil {
			total.Add(storage.ReconcileStats{Errors: 1, ErrorText: []string{err.Error()}})
			return total
		}
	}
	for _, u := range users {
		st, err := r.fs.ReconcileUser(ctx, u)
		if err != nil && ctx.Err() == nil {
			st.Add(storage.ReconcileStats{Errors: 1, ErrorText: []string{err.Error()}})
		}
		total.Add(st)
	}
	return total
}

func target(req db.RescanRequest) string {
	if !req.UserID.Valid {
		return "all users"
	}
	return db.UUIDString(req.UserID)
}
