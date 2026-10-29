package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"discodrive/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EventHub maintains a single LISTEN connection to Postgres and an in-memory
// subscriber registry keyed by userID. Events are produced by the change_log_notify
// trigger (pg_notify 'userID:seq').
type EventHub struct {
	pool *pgxpool.Pool
	mu   sync.Mutex
	subs map[string]map[chan int64]struct{}
}

func NewEventHub(pool *pgxpool.Pool) *EventHub {
	return &EventHub{pool: pool, subs: make(map[string]map[chan int64]struct{})}
}

// Run listens on the change_log channel and fans out notifications to subscribers.
// Reconnects on error. Blocks until ctx is cancelled.
func (h *EventHub) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := h.listen(ctx); err != nil && ctx.Err() == nil {
			log.Printf("discodrive: SSE LISTEN: %v (reconnecting in 1s)", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}
}

func (h *EventHub) listen(ctx context.Context) error {
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN change_log"); err != nil {
		return err
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		userID, seq, ok := parseChangePayload(n.Payload)
		if ok {
			h.dispatch(userID, seq)
		}
	}
}

// parseChangePayload parses the 'userID:seq' notification payload.
func parseChangePayload(p string) (string, int64, bool) {
	i := strings.LastIndexByte(p, ':')
	if i < 0 {
		return "", 0, false
	}
	seq, err := strconv.ParseInt(p[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return p[:i], seq, true
}

func (h *EventHub) dispatch(userID string, seq int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[userID] {
		select {
		case ch <- seq:
		default:
		}
	}
}

// Subscribe registers a subscriber for userID; returns the channel and an unsubscribe function.
func (h *EventHub) Subscribe(userID string) (<-chan int64, func()) {
	ch := make(chan int64, 1)
	h.mu.Lock()
	if h.subs[userID] == nil {
		h.subs[userID] = make(map[chan int64]struct{})
	}
	h.subs[userID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if set := h.subs[userID]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(h.subs, userID)
			}
		}
		close(ch)
	}
}

// formatSSEEvent formats a new-seq SSE event.
func formatSSEEvent(seq int64) string {
	return fmt.Sprintf("data: {\"seq\":%d}\n\n", seq)
}

// GET /sync/events (JWT) — SSE stream of change notifications. The daemon keeps this
// connection open and fetches the delta via /sync/changes?since= on each event.
func (s *Server) handleSyncEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.events.Subscribe(auth.UserID(r.Context()))
	defer unsub()

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	streamEvents(r.Context(), ch, func(s string) {
		fmt.Fprint(w, s)
		flusher.Flush()
	}, sseCoalesce, 25*time.Second)
}

// sseCoalesce is the shortest gap between two events of one stream. A bulk change (a
// rescan importing tens of thousands of files) is one notification per file; a client
// refreshes on every event, so they go out as one event per window, with the latest seq.
const sseCoalesce = 500 * time.Millisecond

// streamEvents writes the events of ch until ctx ends: the first one after a quiet
// spell at once, the rest at most once per window, carrying the latest seq.
func streamEvents(ctx context.Context, ch <-chan int64, write func(string), window, keepaliveEvery time.Duration) {
	keepalive := time.NewTicker(keepaliveEvery)
	defer keepalive.Stop()
	var (
		pending  int64
		flush    <-chan time.Time
		lastSent time.Time
	)
	send := func(seq int64) {
		write(formatSSEEvent(seq))
		lastSent = time.Now()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case seq := <-ch:
			switch {
			case flush != nil:
				pending = max(pending, seq)
			case time.Since(lastSent) >= window:
				send(seq)
			default:
				pending = seq
				flush = time.After(window - time.Since(lastSent))
			}
		case <-flush:
			flush = nil
			send(pending)
		case <-keepalive.C:
			write(": keepalive\n\n")
		}
	}
}
