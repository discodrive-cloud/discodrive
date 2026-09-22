package quota

import (
	"context"
	"crypto/sha256"
	"discodrive/internal/db"
	"encoding/binary"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"time"
)

var ErrReservation = errors.New("upload reservation unavailable")

type Reservation struct {
	c    *Checker
	conn *db.UploadConnection
	q    *db.Queries
	ctx  context.Context
	key  int64
	Row  db.UploadReservation
}

func (c *Checker) CreateReservation(ctx context.Context, id string, user pgtype.UUID) error {
	if c == nil {
		return nil
	}
	return c.q.CreateUploadReservation(ctx, db.CreateUploadReservationParams{ID: id, UserID: user})
}

// A per-upload session lock protects disk I/O from abort/GC in other processes.
// The global quota lock is held only during short accounting transactions, never
// while waiting for the network. Every reservation survives crashes in PostgreSQL.
func (c *Checker) LockReservation(ctx context.Context, id string) (*Reservation, error) {
	conn, err := c.q.AcquireConnection(ctx)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte("discodrive-upload:" + id))
	key := int64(binary.BigEndian.Uint64(hash[:8]))
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&locked); err != nil {
		// Lock acquisition is uncertain after cancellation; destroy the session.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = conn.Hijack().Close(cleanup)
		cancel()
		return nil, err
	}
	if !locked {
		conn.Release()
		return nil, db.ErrUploadConnectionsBusy
	}
	r := &Reservation{c: c, conn: conn, q: db.New(conn), ctx: ctx, key: key}
	r.Row, err = r.q.GetUploadReservation(ctx, id)
	if err != nil {
		r.Close()
		return nil, ErrReservation
	}
	return r, nil
}
func (r *Reservation) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var unlocked bool
	if err := r.conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", r.key).Scan(&unlocked); err != nil || !unlocked {
		// Never put a connection carrying our session lock back into the pool.
		conn := r.conn.Hijack()
		_ = conn.Close(ctx)
		return
	}
	r.conn.Release()
}
func (r *Reservation) Reader(source io.Reader) io.Reader {
	return &reservationReader{reservation: r, source: source}
}
func (r *Reservation) Trim(bytes int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.q.TrimUploadReservation(ctx, db.TrimUploadReservationParams{ID: r.Row.ID, Bytes: bytes})
}
func (r *Reservation) Touch() error { return r.q.TouchUploadReservation(r.ctx, r.Row.ID) }

// Release must only be called after the staged file has been removed or published.
func (r *Reservation) Release() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Publication may have been cancelled while rolling back its transaction.
	if r.conn.Conn.Conn().PgConn().TxStatus() != 'I' {
		if _, err := r.conn.Exec(ctx, "ROLLBACK"); err != nil {
			return err
		}
	}
	return r.q.DeleteUploadReservation(ctx, r.Row.ID)
}
func (c *Checker) StaleReservations(ctx context.Context, cutoff time.Time) ([]db.UploadReservation, error) {
	return c.q.ListStaleUploadReservations(ctx, pgtype.Timestamptz{Time: cutoff, Valid: true})
}

type reservationReader struct {
	reservation *Reservation
	source      io.Reader
}

func (r *reservationReader) Read(p []byte) (int, error) {
	n, readErr := r.source.Read(p)
	if n == 0 {
		return n, readErr
	}
	v := r.reservation
	tx, err := v.conn.Begin(v.ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	q := db.New(tx)
	if err = q.LockUploadQuota(v.ctx); err != nil {
		return 0, err
	}
	// Read the sums AFTER acquiring the lock, using a fresh statement/snapshot.
	checker := &Checker{q: q, total: v.c.total}
	available, err := checker.Allowance(v.ctx, v.Row.UserID)
	if err != nil {
		return 0, err
	}
	if int64(n) > available {
		return 0, exceeded(available)
	}
	if err = q.AddUploadReservation(v.ctx, db.AddUploadReservationParams{ID: v.Row.ID, Bytes: int64(n)}); err != nil {
		return 0, err
	}
	if err = tx.Commit(v.ctx); err != nil {
		return 0, err
	}
	return n, readErr
}

type creditKey struct{}
type uploadCredit struct {
	id    string
	user  pgtype.UUID
	bytes int64
}

// WithCredit identifies the already-accounted file for zero-copy publication.
// The caller holds the upload lock; the reservation stays charged until publication.
func (r *Reservation) WithCredit(ctx context.Context, bytes int64) context.Context {
	return context.WithValue(ctx, creditKey{}, uploadCredit{id: r.Row.ID, user: r.Row.UserID, bytes: bytes})
}

// Connection and Checker let completion publish on the pinned connection rather
// than wait for another pool slot while holding the upload lock.
func (r *Reservation) Connection() *pgxpool.Conn { return r.conn.Conn }
func (r *Reservation) Queries() *db.Queries      { return r.q }
func (r *Reservation) Checker() *Checker         { return &Checker{q: r.q, total: r.c.total} }

func (c *Checker) HasReservation(ctx context.Context, id string) (bool, error) {
	_, err := c.q.GetUploadReservation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func Credit(ctx context.Context, owner pgtype.UUID) (string, int64, bool) {
	value, ok := ctx.Value(creditKey{}).(uploadCredit)
	return value.id, value.bytes, ok && value.user == owner
}

// CancelEmptyReservation is for a just-created internal staging operation that
// could not acquire a connection, before any file was opened or bytes reserved.
func (c *Checker) CancelEmptyReservation(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.q.DeleteEmptyUploadReservation(ctx, id)
}
