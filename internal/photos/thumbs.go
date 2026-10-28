package photos

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/photos/media"
	"discodrive/internal/storage"
)

// ErrRenderFailed is returned by Ensure for a thumbnail that already failed: the
// failure stands until the file changes (which puts the asset back to pending).
var ErrRenderFailed = errors.New("photos: thumbnail failed earlier")

// ErrEconomy is returned for work that only background workers do (video transcoding)
// when the server runs in economy mode (no workers).
var ErrEconomy = errors.New("photos: economy mode, no background workers")

const (
	pollInterval = 2 * time.Second
	maxErrLen    = 500
)

// Thumbs makes the derivatives of assets: grid thumbnails in the background, viewer
// previews on demand, web-playable video copies from a queue.
//
// With workers=0 (economy mode) nothing runs in the background; Ensure builds what a
// request needs, one render at a time.
type Thumbs struct {
	q           *db.Queries
	storageRoot string
	tools       media.Tools
	exif        *media.Exif
	workers     int

	sem chan struct{} // renders started by Ensure

	kicks  []chan struct{} // one per thumbnail worker
	tcKick chan struct{}   // wakes the transcode goroutine

	mu       sync.Mutex
	inflight map[pgtype.UUID]bool // ids claimed by a worker, for the claim queries
	flights  map[string]*flight   // renders in progress, by asset and kind
	claimMu  sync.Mutex           // claim + registering in inflight is one step

	renders atomic.Int64 // renders started, for tests
}

type flight struct {
	done chan struct{}
	path string
	err  error
}

// NewThumbs creates the derivative queue. exif re-reads the metadata a render needs
// (a RAW's embedded preview, a video's duration); it is appended after the original
// arguments and may be nil only if Ensure and the workers are never used.
func NewThumbs(q *db.Queries, storageRoot string, tools media.Tools, workers int, exif *media.Exif) *Thumbs {
	if workers < 0 {
		workers = 0
	}
	n := max(workers, 1)
	t := &Thumbs{tcKick: make(chan struct{}, 1), q: q, storageRoot: storageRoot, tools: tools, exif: exif, workers: workers,
		sem:      make(chan struct{}, n),
		inflight: map[pgtype.UUID]bool{}, flights: map[string]*flight{}}
	for i := 0; i < workers; i++ {
		t.kicks = append(t.kicks, make(chan struct{}, 1))
	}
	return t
}

// Kick wakes the workers: new pending rows exist. It never blocks.
func (t *Thumbs) Kick() {
	if t.workers > 0 {
		select {
		case t.tcKick <- struct{}{}:
		default:
		}
	}
	for _, k := range t.kicks {
		select {
		case k <- struct{}{}:
		default:
		}
	}
}

// Run starts the workers and blocks until ctx ends. In economy mode it only waits.
func (t *Thumbs) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < t.workers; i++ {
		wg.Add(1)
		go func(kick chan struct{}) {
			defer wg.Done()
			t.work(ctx, kick, false)
		}(t.kicks[i])
	}
	if t.workers > 0 {
		// Transcodes get their own goroutine so a long one never holds up thumbnails.
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.work(ctx, t.tcKick, true)
		}()
	}
	wg.Wait()
	<-ctx.Done()
}

func (t *Thumbs) work(ctx context.Context, kick chan struct{}, transcode bool) {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		for t.step(ctx, transcode) {
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-kick:
		case <-tick.C:
		}
	}
}

// step does one job (a thumbnail, or a transcode on the transcode goroutine) and
// reports whether it found one.
func (t *Thumbs) step(ctx context.Context, transcode bool) bool {
	a, ok := t.claim(ctx, transcode)
	if !ok {
		return false
	}
	defer t.release(a.ID)
	// A file that panics a renderer is recorded as failed: left pending it would be
	// claimed again and again, and the panic must not take the server down.
	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("render panicked: %v", r)
			if transcode {
				log.Printf("discodrive: photos: transcode %s: %v", db.UUIDString(a.ID), err)
				_ = t.q.SetTranscodeState(ctx, db.SetTranscodeStateParams{ID: a.ID, TranscodeState: pgtype.Text{String: "failed", Valid: true}})
			} else {
				t.failThumb(ctx, a, err)
			}
		}
	}()
	if transcode {
		t.runTranscode(ctx, a)
	} else {
		t.renderThumb(ctx, a)
	}
	return true
}

func (t *Thumbs) claim(ctx context.Context, transcode bool) (db.PhotoAsset, bool) {
	t.claimMu.Lock()
	defer t.claimMu.Unlock()
	t.mu.Lock()
	exclude := make([]pgtype.UUID, 0, len(t.inflight))
	for id := range t.inflight {
		exclude = append(exclude, id)
	}
	t.mu.Unlock()

	var rows []db.PhotoAsset
	var err error
	if transcode {
		rows, err = t.q.ClaimPendingTranscodes(ctx, db.ClaimPendingTranscodesParams{Exclude: exclude, MaxRows: 1})
	} else {
		rows, err = t.q.ClaimPendingThumbs(ctx, db.ClaimPendingThumbsParams{Exclude: exclude, MaxRows: 1})
	}
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("discodrive: photos: claiming work: %v", err)
		}
		return db.PhotoAsset{}, false
	}
	if len(rows) == 0 {
		return db.PhotoAsset{}, false
	}
	t.mu.Lock()
	t.inflight[rows[0].ID] = true
	t.mu.Unlock()
	return rows[0], true
}

func (t *Thumbs) release(id pgtype.UUID) {
	t.mu.Lock()
	delete(t.inflight, id)
	t.mu.Unlock()
}

// renderThumb renders a claimed asset's thumbnail and records the outcome.
func (t *Thumbs) renderThumb(ctx context.Context, a db.PhotoAsset) {
	_, _ = t.build(ctx, a, "thumb", nil) // a failure is recorded by build; shutdown leaves the row pending
}

func (t *Thumbs) failThumb(ctx context.Context, a db.PhotoAsset, err error) {
	log.Printf("discodrive: photos: thumbnail %s: %v", db.UUIDString(a.ID), err)
	if e := t.q.SetThumbState(ctx, db.SetThumbStateParams{ID: a.ID, ThumbState: "failed",
		ThumbError: pgtype.Text{String: truncate(err.Error(), maxErrLen), Valid: true}}); e != nil && ctx.Err() == nil {
		log.Printf("discodrive: photos: recording failure of %s: %v", db.UUIDString(a.ID), e)
	}
}

func (t *Thumbs) runTranscode(ctx context.Context, a db.PhotoAsset) {
	_, err := t.build(ctx, a, "h264", nil)
	if ctx.Err() != nil {
		return
	}
	state := "ready"
	if err != nil {
		state = "failed"
		log.Printf("discodrive: photos: transcode %s: %v", db.UUIDString(a.ID), err)
	}
	if e := t.q.SetTranscodeState(ctx, db.SetTranscodeStateParams{ID: a.ID, TranscodeState: pgtype.Text{String: state, Valid: true}}); e != nil && ctx.Err() == nil {
		log.Printf("discodrive: photos: recording transcode of %s: %v", db.UUIDString(a.ID), e)
	}
}

// QueueTranscode asks for the web-playable copy of a video. It is a no-op for a video
// that is queued or done, and for anything that is not a video. In economy mode there
// is no one to do it: ErrEconomy.
func (t *Thumbs) QueueTranscode(ctx context.Context, assetID pgtype.UUID) error {
	if t.workers == 0 {
		return ErrEconomy
	}
	n, err := t.q.QueuePhotoTranscode(ctx, assetID)
	if err != nil {
		return err
	}
	if n > 0 {
		t.Kick()
	}
	return nil
}

// Ensure returns the path of the asset's derivative of the given kind ("thumb" or
// "preview"), building it first when it is missing. Concurrent calls for the same
// asset and kind share one render.
func (t *Thumbs) Ensure(ctx context.Context, a db.PhotoAsset, kind string) (string, error) {
	if kind != "thumb" && kind != "preview" {
		return "", fmt.Errorf("photos: unknown derivative kind %q", kind)
	}
	if kind == "thumb" && a.ThumbState == "failed" {
		return "", ErrRenderFailed
	}
	dst := AssetDerivPath(t.storageRoot, a, kind)
	if kind == "preview" || a.ThumbState == "ready" {
		if _, err := os.Stat(dst); err == nil {
			return dst, nil
		}
	}
	return t.build(ctx, a, kind, t.sem)
}

// build renders one derivative, at most once at a time per asset and kind: callers that
// arrive meanwhile wait for that render and share its result. A finished thumbnail is
// recorded as ready.
//
// sem, when not nil, bounds the renders that requests start (a worker has its own limit):
// the leader holds a slot while it renders, and a call that joined needs none.
func (t *Thumbs) build(ctx context.Context, a db.PhotoAsset, kind string, sem chan struct{}) (string, error) {
	key := db.UUIDString(a.ID) + "/" + kind
	t.mu.Lock()
	if f, ok := t.flights[key]; ok {
		t.mu.Unlock()
		select {
		case <-f.done:
			return f.path, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	t.flights[key] = f
	t.mu.Unlock()

	// A panic must not leave the key registered: joined callers would wait forever.
	f.err = errors.New("photos: render panicked")
	defer func() {
		t.mu.Lock()
		delete(t.flights, key)
		t.mu.Unlock()
		close(f.done)
	}()
	f.path, f.err = t.renderLimited(ctx, a, kind, sem)
	if f.err != nil && kind == "thumb" && ctx.Err() == nil {
		t.failThumb(ctx, a, f.err)
	}
	return f.path, f.err
}

func (t *Thumbs) renderLimited(ctx context.Context, a db.PhotoAsset, kind string, sem chan struct{}) (string, error) {
	if sem == nil {
		return t.render(ctx, a, kind)
	}
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	// The file may have appeared while this call waited for its turn.
	if kind == "preview" {
		dst := AssetDerivPath(t.storageRoot, a, kind)
		if _, err := os.Stat(dst); err == nil {
			return dst, nil
		}
	}
	return t.render(ctx, a, kind)
}

func (t *Thumbs) render(ctx context.Context, a db.PhotoAsset, kind string) (string, error) {
	t.renders.Add(1)
	node, err := t.q.GetNodeAnyState(ctx, a.NodeID)
	if err != nil {
		return "", fmt.Errorf("node: %w", err)
	}
	if !node.DiskPath.Valid {
		return "", errors.New("node has no file")
	}
	src := filepath.Join(t.storageRoot, node.DiskPath.String)
	if node.DeletedAt.Valid && node.TrashPath.Valid {
		src = filepath.Join(t.storageRoot, node.TrashPath.String) // the gallery trash shows thumbnails
	}
	// The renderers open the file by name; refuse a path that leaves the storage root
	// through a symlink first, as the indexer does.
	f, err := storage.NewLocalDisk(t.storageRoot).OpenAbsolute(src)
	if err != nil {
		return "", err
	}
	_ = f.Close()
	dst := AssetDerivPath(t.storageRoot, a, kind)

	if kind == "h264" {
		if err := media.TranscodeH264(ctx, t.tools, src, dst); err != nil {
			return "", err
		}
		return dst, nil
	}
	// A failed read still yields the kind from the extension; the renderer reports what
	// it really cannot do without.
	meta, _ := t.exif.Read(ctx, src)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if kind == "thumb" {
		err = media.Thumb(ctx, t.tools, src, meta, dst)
	} else {
		err = media.Preview(ctx, t.tools, src, meta, dst)
	}
	if err != nil {
		return "", err
	}
	if kind == "thumb" {
		// The thumbnail is there; a failure to record it only leaves the row pending (it is
		// claimed and rendered again later), it must not mark a good file as failed.
		if e := t.q.SetThumbState(ctx, db.SetThumbStateParams{ID: a.ID, ThumbState: "ready"}); e != nil && ctx.Err() == nil {
			log.Printf("discodrive: photos: recording thumbnail of %s: %v", db.UUIDString(a.ID), e)
		}
	}
	return dst, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
