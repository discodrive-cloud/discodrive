package photos

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	failed   map[string]time.Time // previews that did not render, by derivative path
	claimMu  sync.Mutex           // claim + registering in inflight is one step

	renders atomic.Int64 // renders started, for tests

	hashPause   time.Duration // pause between assets of the hash backfill
	hashRetry   time.Duration // wait after the backfill failed to list its work
	backfilling atomic.Bool   // the hash backfill is running

	// OnReady, when set (before Run), is called after a thumbnail was recorded as ready;
	// it must not block (Faces.Kick: the card can be looked at for faces now).
	OnReady func()
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
	t := &Thumbs{hashPause: hashBackfillPause, hashRetry: hashBackfillRetry, tcKick: make(chan struct{}, 1), q: q, storageRoot: storageRoot, tools: tools, exif: exif, workers: workers,
		sem:      make(chan struct{}, n),
		inflight: map[pgtype.UUID]bool{}, flights: map[string]*flight{}, failed: map[string]time.Time{}}
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

// Run starts the workers and blocks until ctx ends. In economy mode only the hash
// backfill runs (it reads small thumbnails, never the originals).
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		t.backfillHashes(ctx)
	}()
	wg.Wait()
	<-ctx.Done()
}

const (
	hashBackfillBatch = 200
	hashBackfillPause = 50 * time.Millisecond
	hashBackfillRetry = time.Minute
)

// Backfilling reports whether the hash backfill is still going through the library: the
// duplicates finder says it is still looking meanwhile.
func (t *Thumbs) Backfilling() bool { return t.backfilling.Load() }

// thumbHash hashes a finished thumbnail. A failure is only logged (valid=false): the hash
// is for the duplicates finder and the thumbnail stands without it.
func thumbHash(a db.PhotoAsset, thumbPath string) (h pgtype.Int8, flat bool) {
	v, flat, err := DHashFile(thumbPath)
	if err != nil {
		log.Printf("discodrive: photos: hashing %s: %v", db.UUIDString(a.ID), err)
		return pgtype.Int8{}, false
	}
	return pgtype.Int8{Int64: int64(v), Valid: true}, flat
}

// backfillHashes hashes the thumbnails that were made before hashes were kept, from the
// files on disk (no renders, the originals are not opened). It pages by id within the
// pass, so a card with no thumbnail file is skipped, not retried, and ends on an empty page:
// thumbnails made later are hashed as they render. A database error is waited out
// (hashRetry) and the pass goes on where it stopped.
func (t *Thumbs) backfillHashes(ctx context.Context) {
	t.backfilling.Store(true)
	defer t.backfilling.Store(false)
	after := pgtype.UUID{Valid: true}
	for {
		rows, err := t.q.ListPhotoHashBackfill(ctx, db.ListPhotoHashBackfillParams{AfterID: after, MaxRows: hashBackfillBatch})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("discodrive: photos: listing hashes to fill (retrying in %v): %v", t.hashRetry, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(t.hashRetry):
			}
			continue
		}
		if len(rows) == 0 {
			return
		}
		for _, a := range rows {
			after = a.ID
			p := AssetDerivPath(t.storageRoot, a, "thumb")
			if exists(p) {
				if h, flat := thumbHash(a, p); h.Valid {
					if err := t.q.SetPhotoHash(ctx, db.SetPhotoHashParams{ID: a.ID, Phash: h, PhashFlat: flat,
						Rotation: a.Rotation, NodeVersion: a.NodeVersion}); err != nil && ctx.Err() == nil {
						log.Printf("discodrive: photos: recording hash of %s: %v", db.UUIDString(a.ID), err)
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(t.hashPause):
			}
		}
	}
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
	// A file that did not render as a thumbnail will not render as a preview either.
	if a.ThumbState == "failed" {
		return "", ErrRenderFailed
	}
	dst := AssetDerivPath(t.storageRoot, a, kind)
	if kind == "preview" && t.previewFailed(dst) {
		return "", ErrRenderFailed
	}
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
	// The key names the picture, not just the card: a render of the file as it was before
	// a rotation or an overwrite is not the one a later request is waiting for.
	key := fmt.Sprintf("%s/%s/%d/%d", db.UUIDString(a.ID), kind, a.NodeVersion.Int64, a.Rotation)
	t.mu.Lock()
	if f, ok := t.flights[key]; ok {
		t.mu.Unlock()
		select {
		case <-f.done:
			// The render belonged to the request that started it. If that request went
			// away (a swipe past the photo) while this one is still here, its
			// cancellation is not this one's answer: render again.
			if f.err != nil && ctx.Err() == nil &&
				(errors.Is(f.err, context.Canceled) || errors.Is(f.err, context.DeadlineExceeded)) {
				return t.build(ctx, a, kind, sem)
			}
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
	if f.err != nil && ctx.Err() == nil {
		switch kind {
		case "thumb":
			t.failThumb(ctx, a, f.err)
		case "preview":
			t.failPreview(AssetDerivPath(t.storageRoot, a, kind))
		}
	}
	return f.path, f.err
}

// previewRetry is how long a preview that failed to render is not tried again. The
// derivative path names the file version and rotation, so a changed file is tried at
// once; anything else (a tool installed later) after this long.
const previewRetry = time.Hour

// failPreview remembers a preview that did not render: requests for it (anonymous ones
// through album links among them) must not start the same failing render each time.
func (t *Thumbs) failPreview(dst string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if len(t.failed) >= 10000 {
		for k, at := range t.failed {
			if now.Sub(at) > previewRetry {
				delete(t.failed, k)
			}
		}
	}
	t.failed[dst] = now
}

func (t *Thumbs) previewFailed(dst string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.failed[dst]
	return ok && time.Since(at) < previewRetry
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
	src, err := t.sourceOf(ctx, a)
	if err != nil {
		return "", err
	}
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
	meta.Rotate = int(a.Rotation)
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
		// claimed and rendered again later), it must not mark a good file as failed. The
		// hash goes in the same write, so the card is never ready with an old picture's hash.
		h, flat := thumbHash(a, dst)
		if e := t.q.SetThumbReady(ctx, db.SetThumbReadyParams{
			ID: a.ID, Phash: h, PhashFlat: flat, Rotation: a.Rotation, NodeVersion: a.NodeVersion,
		}); e != nil && ctx.Err() == nil {
			log.Printf("discodrive: photos: recording thumbnail of %s: %v", db.UUIDString(a.ID), e)
		} else if e == nil && t.OnReady != nil {
			t.OnReady()
		}
	}
	return dst, nil
}

// sourceOf returns the absolute path of the asset's file (in the trash when it is
// there: the gallery trash shows thumbnails). The renderers open the file by name, so a
// path that leaves the storage root through a symlink is refused first, as the indexer does.
func (t *Thumbs) sourceOf(ctx context.Context, a db.PhotoAsset) (string, error) {
	node, err := t.q.GetNodeAnyState(ctx, a.NodeID)
	if err != nil {
		return "", fmt.Errorf("node: %w", err)
	}
	if !node.DiskPath.Valid {
		return "", errors.New("node has no file")
	}
	src := filepath.Join(t.storageRoot, node.DiskPath.String)
	if node.DeletedAt.Valid && node.TrashPath.Valid {
		src = filepath.Join(t.storageRoot, node.TrashPath.String)
	}
	f, err := storage.NewLocalDisk(t.storageRoot).OpenAbsolute(src)
	if err != nil {
		return "", err
	}
	_ = f.Close()
	return src, nil
}

// PreviewInto renders the asset's viewer preview into dst (a scratch file: the face
// queue sends it to the face service and removes it), unless the kept preview exists, in
// which case that path is returned and dst is not written.
func (t *Thumbs) PreviewInto(ctx context.Context, a db.PhotoAsset, dst string) (string, error) {
	if kept := AssetDerivPath(t.storageRoot, a, "preview"); exists(kept) {
		return kept, nil
	}
	src, err := t.sourceOf(ctx, a)
	if err != nil {
		return "", err
	}
	meta, _ := t.exif.Read(ctx, src)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	meta.Rotate = int(a.Rotation)
	if err := media.Preview(ctx, t.tools, src, meta, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// Rotated clears what was made from the asset before the user turned it: the old
// previews (their name carries the turn) and the pictures of its faces (cut from the old
// preview; the boxes turned with it). The thumbnail is pending again and made in place.
func (t *Thumbs) Rotated(ctx context.Context, a db.PhotoAsset) {
	dropStaleDerivatives(t.storageRoot, a)
	faces, err := t.q.ListAssetFaces(ctx, a.ID)
	if err != nil {
		return
	}
	dir := filepath.Join(derivUserDir(t.storageRoot, db.UUIDString(a.UserID)), "faces")
	for _, f := range faces {
		id := db.UUIDString(f.ID)
		_ = os.Remove(filepath.Join(dir, shard(id), id+".jpg"))
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// FaceThumb returns the picture of a face of the asset (box in fractions of the viewer
// preview), cut from the preview on first ask and kept under the face's id: a face row is
// replaced, never changed, when the file's content changes.
func (t *Thumbs) FaceThumb(ctx context.Context, a db.PhotoAsset, faceID string, x1, y1, x2, y2 float32) (string, error) {
	if faceID == "" || strings.ContainsAny(faceID, `/\.`) {
		return "", errors.New("photos: bad face id")
	}
	dst := filepath.Join(derivUserDir(t.storageRoot, db.UUIDString(a.UserID)), "faces", shard(faceID), faceID+".jpg")
	if exists(dst) {
		return dst, nil
	}
	preview, err := t.Ensure(ctx, a, "preview")
	if err != nil {
		return "", err
	}
	if err := media.FaceCrop(ctx, t.tools, preview, x1, y1, x2, y2, dst); err != nil {
		return "", err
	}
	return dst, nil
}
