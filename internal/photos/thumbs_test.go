package photos

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

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
