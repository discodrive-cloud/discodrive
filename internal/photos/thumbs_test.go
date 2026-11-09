package photos

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

func (e *env) thumbs(workers int) *Thumbs {
	return NewThumbs(e.q, e.root, e.tools, workers, e.ix.exif)
}

func (e *env) deriv(a db.PhotoAsset, kind string) string {
	return AssetDerivPath(e.root, a, kind)
}

func (e *env) reload(a db.PhotoAsset) db.PhotoAsset {
	e.t.Helper()
	got, err := e.q.GetPhotoAsset(context.Background(), db.GetPhotoAssetParams{ID: a.ID, UserID: a.UserID})
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestThumbs_WorkerProcessesPending(t *testing.T) {
	e := newEnv(t)
	src := e.source("Photos")
	var nodes []db.Node
	for _, n := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		nodes = append(nodes, e.push(&[]string{id(src)}[0], n, e.jpeg(n, 64)))
	}
	e.catchUp()

	th := e.thumbs(2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { th.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	th.Kick()

	for _, n := range nodes {
		a := e.mustAsset(n)
		waitFor(t, n.Name+" ready", func() bool { return e.reload(a).ThumbState == "ready" })
		if !exists(e.deriv(a, "thumb")) {
			t.Fatalf("%s: thumbnail file missing", n.Name)
		}
		if exists(e.deriv(a, "preview")) {
			t.Fatalf("%s: preview must stay lazy", n.Name)
		}
	}
}

func TestThumbs_EnsureLazyInEconomyMode(t *testing.T) {
	e := newEnv(t)
	src := e.source("Photos")
	n1 := e.push(&[]string{id(src)}[0], "a.jpg", e.jpeg("a.jpg", 64))
	n2 := e.push(&[]string{id(src)}[0], "b.jpg", e.jpeg("b.jpg", 64))
	e.catchUp()
	a1, a2 := e.mustAsset(n1), e.mustAsset(n2)

	th := e.thumbs(0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { th.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	th.Kick()
	time.Sleep(300 * time.Millisecond)
	if e.reload(a1).ThumbState != "pending" || th.renders.Load() != 0 {
		t.Fatal("economy mode rendered in the background")
	}

	p, err := th.Ensure(ctx, a1, "preview")
	if err != nil || !exists(p) || p != e.deriv(a1, "preview") {
		t.Fatalf("preview: %q %v", p, err)
	}
	p, err = th.Ensure(ctx, a1, "thumb")
	if err != nil || !exists(p) {
		t.Fatalf("thumb: %q %v", p, err)
	}
	if e.reload(a1).ThumbState != "ready" {
		t.Fatal("Ensure did not record the thumbnail")
	}
	if e.reload(a2).ThumbState != "pending" {
		t.Fatal("the other asset must stay pending")
	}

	// A missing file of a ready thumbnail is rebuilt.
	_ = os.Remove(p)
	if _, err := th.Ensure(ctx, e.reload(a1), "thumb"); err != nil || !exists(p) {
		t.Fatalf("rebuild: %v", err)
	}

	if err := th.QueueTranscode(ctx, a1.ID); err != ErrEconomy {
		t.Fatalf("QueueTranscode in economy mode: %v", err)
	}
	if _, err := th.Ensure(ctx, a1, "bogus"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestThumbs_EnsureSharesConcurrentRenders(t *testing.T) {
	e := newEnv(t)
	src := e.source("Photos")
	a := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "a.jpg", e.jpeg("a.jpg", 64)))
	th := e.thumbs(0)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := th.Ensure(context.Background(), a, "preview"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// Late arrivals find the file; overlapping ones join the render: far fewer than 8.
	if n := th.renders.Load(); n > 2 {
		t.Fatalf("%d renders for one asset", n)
	}
}

func (e *env) mustAssetAfter(n db.Node) db.PhotoAsset {
	e.catchUp()
	return e.mustAsset(n)
}

func TestThumbs_FailureRecorded(t *testing.T) {
	e := newEnv(t)
	src := e.source("Photos")
	bad := filepathWrite(t, "bad.jpg", []byte("this is not a picture"))
	nBad := e.push(&[]string{id(src)}[0], "bad.jpg", bad)
	nOK := e.push(&[]string{id(src)}[0], "ok.jpg", e.jpeg("ok.jpg", 64))
	e.catchUp()

	th := e.thumbs(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { th.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	th.Kick()

	aBad, aOK := e.mustAsset(nBad), e.mustAsset(nOK)
	waitFor(t, "all settled", func() bool {
		return e.reload(aBad).ThumbState != "pending" && e.reload(aOK).ThumbState != "pending"
	})
	if got := e.reload(aOK).ThumbState; got != "ready" {
		t.Fatalf("good file: %s", got)
	}
	b := e.reload(aBad)
	if b.ThumbState != "failed" || !b.ThumbError.Valid || b.ThumbError.String == "" || len(b.ThumbError.String) > 500 {
		t.Fatalf("bad file: %s %+v", b.ThumbState, b.ThumbError)
	}
}

func TestThumbs_TranscodeQueue(t *testing.T) {
	e := newEnv(t)
	src := e.source("Photos")
	n := e.push(&[]string{id(src)}[0], "v.mov", e.mov("v.mov"))
	e.catchUp()
	a := e.mustAsset(n)

	th := e.thumbs(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { th.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	if err := th.QueueTranscode(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "transcode", func() bool { return e.reload(a).TranscodeState.String == "ready" })
	if !exists(e.deriv(a, "h264")) {
		t.Fatal("h264 file missing")
	}
	waitFor(t, "video thumb", func() bool { return e.reload(a).ThumbState == "ready" })
}

// A claim must not hand out a row another worker of this process is rendering.
func TestClaimPendingThumbs_Exclude(t *testing.T) {
	e := newEnv(t)
	src := e.source("Photos")
	n := e.push(&[]string{id(src)}[0], "a.jpg", e.jpeg("a.jpg", 64))
	e.catchUp()
	a := e.mustAsset(n)
	ctx := context.Background()

	rows, err := e.q.ClaimPendingThumbs(ctx, db.ClaimPendingThumbsParams{MaxRows: 5})
	if err != nil || len(rows) != 1 {
		t.Fatalf("first claim: %d %v", len(rows), err)
	}
	rows, err = e.q.ClaimPendingThumbs(ctx, db.ClaimPendingThumbsParams{Exclude: []pgtype.UUID{a.ID}, MaxRows: 5})
	if err != nil || len(rows) != 0 {
		t.Fatalf("excluded row claimed again: %d %v", len(rows), err)
	}
}

func filepathWrite(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := t.TempDir() + "/" + name
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Ensure must not retry a failed thumbnail on every request.
func TestThumbs_EnsureFailedNotRetried(t *testing.T) {
	e := newEnv(t)
	src := e.source("Photos")
	n := e.push(&[]string{id(src)}[0], "bad.jpg", filepathWrite(t, "bad.jpg", []byte("nope")))
	e.catchUp()
	a := e.mustAsset(n)
	th := e.thumbs(0)
	if _, err := th.Ensure(context.Background(), a, "thumb"); err == nil {
		t.Fatal("broken file rendered")
	}
	a = e.reload(a)
	if a.ThumbState != "failed" {
		t.Fatalf("state %s", a.ThumbState)
	}
	before := th.renders.Load()
	for i := 0; i < 3; i++ {
		if _, err := th.Ensure(context.Background(), a, "thumb"); !errors.Is(err, ErrRenderFailed) {
			t.Fatalf("want ErrRenderFailed, got %v", err)
		}
	}
	if th.renders.Load() != before {
		t.Fatal("failed thumbnail was rendered again")
	}
}

// overwrite stores src as the next version of node (an edit in another app, a re-upload).
func (e *env) overwrite(n db.Node, src string) db.Node {
	e.t.Helper()
	f, err := os.Open(src)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	v := n.Version
	rel := n.DiskPath.String[len(e.userID)+1:]
	r, err := e.fs.PushByPathWithMeta(context.Background(), e.userID, rel, &v, f, storage.PushMeta{})
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Node
}

// A file overwritten in place gets a new viewer preview: the old one is never served
// again, and its file goes.
func TestThumbs_PreviewFollowsNewContent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	n := e.push(&[]string{id(src)}[0], "a.jpg", e.jpeg("a.jpg", 64))
	a := e.mustAssetAfter(n)
	th := e.thumbs(0)
	old, err := th.Ensure(ctx, a, "preview")
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := imageWidth(t, e, old); w != 64 {
		t.Fatalf("first preview is %d wide", w)
	}

	e.overwrite(n, e.jpeg("b.jpg", 96))
	b := e.mustAssetAfter(n)
	renders := th.renders.Load()
	p, err := th.Ensure(ctx, b, "preview")
	if err != nil {
		t.Fatal(err)
	}
	if p == old || th.renders.Load() != renders+1 {
		t.Fatalf("preview not rebuilt for the new content: %s (old %s)", p, old)
	}
	if w, _ := imageWidth(t, e, p); w != 96 {
		t.Fatalf("preview of the new content is %d wide, want 96", w)
	}
	if exists(old) {
		t.Fatal("the preview of the old content is still on disk")
	}
	// The same content under a new name keeps its preview.
	if _, err := e.fs.Rename(ctx, e.userID, id(n), "c.jpg"); err != nil {
		t.Fatal(err)
	}
	c := e.mustAssetAfter(n)
	if q, err := th.Ensure(ctx, c, "preview"); err != nil || q != p || th.renders.Load() != renders+1 {
		t.Fatalf("a rename re-rendered the preview: %s %v", q, err)
	}
}

func imageWidth(t *testing.T, e *env, p string) (int, int) {
	t.Helper()
	out, err := exec.Command(e.tools.Exiftool, "-s3", "-n", "-ImageWidth", "-ImageHeight", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		t.Fatalf("exiftool %q", out)
	}
	w, _ := strconv.Atoi(f[0])
	h, _ := strconv.Atoi(f[1])
	return w, h
}

// A turned card gets a turned preview under a new name; the old one goes on Rotated, and
// a rescan of the unchanged file keeps the turn.
func TestThumbs_PreviewFollowsRotation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	n := e.push(&[]string{id(src)}[0], "a.jpg", e.jpeg("a.jpg", 64)) // 64x48
	a := e.mustAssetAfter(n)
	th := e.thumbs(0)
	old, err := th.Ensure(ctx, a, "preview")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.RotatePhotoCards(ctx, db.RotatePhotoCardsParams{UserID: a.UserID, Ids: []pgtype.UUID{a.ID}, Turn: 90}); err != nil {
		t.Fatal(err)
	}
	b := e.reload(a)
	th.Rotated(ctx, b)
	if exists(old) {
		t.Fatal("the unturned preview is still on disk")
	}
	p, err := th.Ensure(ctx, b, "preview")
	if err != nil || p == old {
		t.Fatalf("preview %s %v (old %s)", p, err, old)
	}
	if w, h := imageWidth(t, e, p); w != 48 || h != 64 {
		t.Fatalf("turned preview %dx%d, want 48x64", w, h)
	}
	if err := e.ix.indexNode(ctx, n.ID, true); err != nil {
		t.Fatal(err)
	}
	if c := e.reload(a); c.Rotation != 90 {
		t.Fatalf("a rescan dropped the turn: %d", c.Rotation)
	}
}

// asymmetricJPEG writes a 64x48 picture, bright on the left third and dark elsewhere with a
// gradient, so a quarter turn changes its hash.
func (e *env) asymmetricJPEG(name string) string {
	e.t.Helper()
	img := image.NewGray(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			v := uint8(x * 4)
			if y < 12 {
				v = 255 - v
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	p := filepath.Join(e.t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 95}); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func TestThumbs_HashOnRender(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	a := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "a.jpg", e.asymmetricJPEG("a.jpg")))
	if a.Phash.Valid {
		t.Fatal("hash before render")
	}
	if _, err := e.thumbs(0).Ensure(ctx, a, "thumb"); err != nil {
		t.Fatal(err)
	}
	if !e.reload(a).Phash.Valid {
		t.Fatal("no hash after the thumbnail render")
	}
}

func TestThumbs_HashAfterRotate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	a := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "a.jpg", e.asymmetricJPEG("a.jpg")))
	th := e.thumbs(0)
	if _, err := th.Ensure(ctx, a, "thumb"); err != nil {
		t.Fatal(err)
	}
	before := e.reload(a)
	if !before.Phash.Valid {
		t.Fatal("no hash after the first render")
	}
	if _, err := e.q.RotatePhotoCards(ctx, db.RotatePhotoCardsParams{UserID: a.UserID, Ids: []pgtype.UUID{a.ID}, Turn: 90}); err != nil {
		t.Fatal(err)
	}
	b := e.reload(a)
	th.Rotated(ctx, b)
	if _, err := th.Ensure(ctx, b, "thumb"); err != nil {
		t.Fatal(err)
	}
	after := e.reload(a)
	if !after.Phash.Valid || after.Phash.Int64 == before.Phash.Int64 {
		t.Fatalf("hash did not follow the turn: %v -> %v", before.Phash, after.Phash)
	}
}

func TestThumbs_HashBackfill(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	a := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "a.jpg", e.asymmetricJPEG("a.jpg")))
	missing := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "b.jpg", e.asymmetricJPEG("b.jpg")))
	th := e.thumbs(0)
	for _, x := range []db.PhotoAsset{a, missing} {
		if _, err := th.Ensure(ctx, x, "thumb"); err != nil {
			t.Fatal(err)
		}
	}
	// Forget the hashes; one thumbnail file is gone.
	if _, err := e.pool.Exec(ctx, "UPDATE photo_assets SET phash = NULL"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.deriv(missing, "thumb")); err != nil {
		t.Fatal(err)
	}
	renders := th.renders.Load()
	th.hashPause = 0
	th.backfillHashes(ctx)
	if !e.reload(a).Phash.Valid {
		t.Fatal("backfill did not hash the card")
	}
	if e.reload(missing).Phash.Valid {
		t.Fatal("a card without a thumbnail file got a hash")
	}
	if th.renders.Load() != renders {
		t.Fatal("backfill rendered")
	}
}

func TestThumbs_FlatThumbMarked(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	black := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "black.jpg", e.jpeg("black.jpg", 64)))
	pic := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "a.jpg", e.asymmetricJPEG("a.jpg")))
	th := e.thumbs(0)
	for _, a := range []db.PhotoAsset{black, pic} {
		if _, err := th.Ensure(ctx, a, "thumb"); err != nil {
			t.Fatal(err)
		}
	}
	if b := e.reload(black); !b.Phash.Valid || !b.PhashFlat || b.ThumbState != "ready" {
		t.Fatalf("black frame: phash %v flat %v state %s", b.Phash, b.PhashFlat, b.ThumbState)
	}
	if p := e.reload(pic); !p.Phash.Valid || p.PhashFlat {
		t.Fatalf("picture: phash %v flat %v", p.Phash, p.PhashFlat)
	}
}

func TestThumbs_RenderReplacesStaleHash(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	a := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "a.jpg", e.asymmetricJPEG("a.jpg")))
	// The hash of an earlier picture, and the card pending again (the file changed).
	if _, err := e.pool.Exec(ctx, `UPDATE photo_assets SET phash = 12345, phash_flat = true, thumb_state = 'pending' WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.thumbs(0).Ensure(ctx, e.reload(a), "thumb"); err != nil {
		t.Fatal(err)
	}
	got := e.reload(a)
	if got.ThumbState != "ready" || !got.Phash.Valid || got.Phash.Int64 == 12345 || got.PhashFlat {
		t.Fatalf("after render: state %s phash %v flat %v", got.ThumbState, got.Phash, got.PhashFlat)
	}
}

func TestThumbs_BackfillSkipsChildrenAndStale(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	parent := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "a.jpg", e.asymmetricJPEG("a.jpg")))
	kid := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "b.jpg", e.asymmetricJPEG("b.jpg")))
	th := e.thumbs(0)
	for _, x := range []db.PhotoAsset{parent, kid} {
		if _, err := th.Ensure(ctx, x, "thumb"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE photo_assets SET phash = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE photo_assets SET parent_id = $2, role = 'original' WHERE id = $1`, kid.ID, parent.ID); err != nil {
		t.Fatal(err)
	}
	th.hashPause = 0
	th.backfillHashes(ctx)
	if !e.reload(parent).Phash.Valid {
		t.Fatal("parent not hashed")
	}
	if e.reload(kid).Phash.Valid {
		t.Fatal("a child was hashed: children are never candidates")
	}
	// The backfill's write is refused once the card moved on (turned meanwhile).
	p := e.reload(parent)
	if _, err := e.pool.Exec(ctx, `UPDATE photo_assets SET phash = NULL, rotation = 90 WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.q.SetPhotoHash(ctx, db.SetPhotoHashParams{ID: p.ID, Phash: pgtype.Int8{Int64: 7, Valid: true},
		Rotation: p.Rotation, NodeVersion: p.NodeVersion}); err != nil {
		t.Fatal(err)
	}
	if e.reload(parent).Phash.Valid {
		t.Fatal("a hash of the picture before the turn was written")
	}
}

func TestThumbs_BackfillFlagAndEconomy(t *testing.T) {
	e := newEnv(t)
	src := e.source("Photos")
	a := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "a.jpg", e.asymmetricJPEG("a.jpg")))
	th := e.thumbs(0) // economy mode: the backfill runs all the same
	if _, err := th.Ensure(context.Background(), a, "thumb"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE photo_assets SET phash = NULL`); err != nil {
		t.Fatal(err)
	}
	th.hashPause = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { th.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !th.Backfilling() && !e.reload(a).Phash.Valid {
		if time.Now().After(deadline) {
			t.Fatal("backfill never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for th.Backfilling() {
		if time.Now().After(deadline) {
			t.Fatal("backfill never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !e.reload(a).Phash.Valid {
		t.Fatal("economy mode: not hashed")
	}
	cancel()
	<-done
}

func TestThumbs_BackfillRetriesAfterDBError(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	a := e.mustAssetAfter(e.push(&[]string{id(src)}[0], "a.jpg", e.asymmetricJPEG("a.jpg")))
	th := e.thumbs(0)
	if _, err := th.Ensure(ctx, a, "thumb"); err != nil {
		t.Fatal(err)
	}
	// The listing fails while the column is away; the backfill must wait and go on.
	e.exec(`UPDATE photo_assets SET phash = NULL`)
	e.exec(`ALTER TABLE photo_assets RENAME COLUMN thumb_state TO thumb_state_away`)
	th.hashPause, th.hashRetry = 0, 50*time.Millisecond
	done := make(chan struct{})
	go func() { th.backfillHashes(ctx); close(done) }()
	time.Sleep(200 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("backfill gave up on a database error")
	default:
	}
	e.exec(`ALTER TABLE photo_assets RENAME COLUMN thumb_state_away TO thumb_state`)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("backfill did not finish after the database came back")
	}
	if !e.reload(a).Phash.Valid {
		t.Fatal("not hashed after the retry")
	}
}
