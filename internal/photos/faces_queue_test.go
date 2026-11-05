package photos

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/photos/faces"
)

// fakeML answers every picture with one face in the same place and the same embedding
// (so all pictures show one person); status, when not 200, is returned instead.
type fakeML struct {
	srv    *httptest.Server
	status atomic.Int32
	calls  atomic.Int32
	// wrongModel: every predict is refused as another model (the service is up)
	wrongModel atomic.Bool
	// poison: the first picture seen always gets a 500 (the service is up otherwise)
	poison   atomic.Bool
	poisonMu sync.Mutex
	poisoned string
}

func newFakeML(t *testing.T) *fakeML {
	m := &fakeML{}
	m.status.Store(200)
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		if r.URL.Path == "/ping" {
			if s := m.status.Load(); s != 200 {
				w.WriteHeader(int(s))
				return
			}
			_, _ = io.WriteString(w, "pong")
			return
		}
		if s := m.status.Load(); s != 200 {
			w.WriteHeader(int(s))
			return
		}
		if m.wrongModel.Load() {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"detail":"not served here","code":"unknown_model"}`)
			return
		}
		if m.poison.Load() {
			f, _, err := r.FormFile("image")
			if err == nil {
				b, _ := io.ReadAll(f)
				sum := fmt.Sprintf("%x", sha256.Sum256(b))
				m.poisonMu.Lock()
				if m.poisoned == "" {
					m.poisoned = sum
				}
				bad := m.poisoned == sum
				m.poisonMu.Unlock()
				if bad {
					w.WriteHeader(500)
					return
				}
			}
		}
		_, _ = io.WriteString(w, `{"imageHeight":100,"imageWidth":100,"facial-recognition":[`+
			`{"boundingBox":{"x1":10,"y1":10,"x2":40,"y2":50},"score":0.9,"embedding":"`+
			strings.ReplaceAll(mlEmbedding(1, 2, 3), `"`, `\"`)+`"}]}`)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// facesEnv: three photos with rendered thumbnails, people on, a queue against the fake.
func facesEnv(t *testing.T) (*env, *fakeML, *Faces, []db.Node, context.CancelFunc) {
	e := newEnv(t)
	src := e.source("Photos")
	var nodes []db.Node
	for i, n := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		nodes = append(nodes, e.push(&[]string{id(src)}[0], n, e.jpeg(n, 64+16*i))) // three different pictures
	}
	e.catchUp()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	th := e.thumbs(1)
	go th.Run(ctx)
	th.Kick()
	for _, n := range nodes {
		waitFor(t, "thumbnail", func() bool { return e.mustAsset(n).ThumbState == "ready" })
	}
	if _, err := e.q.SetFacesEnabled(ctx, db.SetFacesEnabledParams{UserID: e.uid, FacesEnabled: true}); err != nil {
		t.Fatal(err)
	}
	ml := newFakeML(t)
	f := NewFaces(e.q, th, &MLClient{URL: ml.srv.URL, Model: "buffalo_l", MinScore: 0.7},
		faces.Params{MaxDistance: 0.5, MinFaces: 3}, 1)
	f.retry = 100 * time.Millisecond
	f.Begin = e.pool.Begin
	return e, ml, f, nodes, cancel
}

func (e *env) people() []db.ListPhotoPeopleRow {
	e.t.Helper()
	p, err := e.q.ListPhotoPeople(context.Background(), db.ListPhotoPeopleParams{UserID: e.uid})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func TestFacesQueueDetectsAndClusters(t *testing.T) {
	e, _, f, nodes, _ := facesEnv(t)
	go f.Run(context.Background())
	f.Kick()
	for _, n := range nodes {
		waitFor(t, "faces", func() bool { return e.mustAsset(n).FaceState.String == "done" })
	}
	waitFor(t, "a person", func() bool { p := e.people(); return len(p) == 1 && p[0].Count == 3 })
	fs, _ := e.q.ListAssetFaces(context.Background(), e.mustAsset(nodes[0]).ID)
	if len(fs) != 1 || fs[0].X1 != 0.1 || fs[0].Y2 != 0.5 || len(fs[0].Embedding) != 4*EmbeddingDim {
		t.Fatalf("stored face %+v", fs)
	}
}

func TestFacesQueueMLDownKeepsPending(t *testing.T) {
	e, ml, f, nodes, _ := facesEnv(t)
	ml.status.Store(503)
	go f.Run(context.Background())
	f.Kick()
	waitFor(t, "a try", func() bool { return ml.calls.Load() > 0 })
	waitFor(t, "offline", func() bool { return !f.Online() })
	time.Sleep(300 * time.Millisecond)
	for _, n := range nodes {
		if a := e.mustAsset(n); a.FaceState.Valid {
			t.Fatalf("state %v while the service is down", a.FaceState)
		}
	}
	ml.status.Store(200)
	for _, n := range nodes {
		waitFor(t, "faces after the service came back", func() bool { return e.mustAsset(n).FaceState.String == "done" })
	}
	if !f.Online() {
		t.Fatal("still offline")
	}
}

func TestFacesQueueRedetectKeepsPerson(t *testing.T) {
	e, _, f, nodes, _ := facesEnv(t)
	ctx := context.Background()
	go f.Run(ctx)
	f.Kick()
	waitFor(t, "a person", func() bool { return len(e.people()) == 1 })
	p := e.people()[0]
	if _, err := e.q.UpdatePhotoPerson(ctx, db.UpdatePhotoPersonParams{ID: p.ID, UserID: e.uid,
		Name: pgtype.Text{String: "Аня", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	old, _ := e.q.ListAssetFaces(ctx, e.mustAsset(nodes[0]).ID)
	if err := e.q.ResetFaceState(ctx, e.uid); err != nil {
		t.Fatal(err)
	}
	f.Kick()
	for _, n := range nodes {
		waitFor(t, "faces again", func() bool { return e.mustAsset(n).FaceState.String == "done" })
	}
	now, _ := e.q.ListAssetFaces(ctx, e.mustAsset(nodes[0]).ID)
	if len(now) != 1 || now[0].ID == old[0].ID {
		t.Fatalf("faces not replaced: %+v", now)
	}
	if now[0].PersonID != p.ID {
		t.Fatalf("person lost: %v, want %v", now[0].PersonID, p.ID)
	}
	if got := e.people(); len(got) != 1 || got[0].Name != "Аня" || got[0].Count != 3 {
		t.Fatalf("people after redetect: %+v", got)
	}
}

func TestFacesQueueSkipsDisabledUserAndChildren(t *testing.T) {
	e, ml, f, nodes, _ := facesEnv(t)
	ctx := context.Background()
	// The third photo becomes the original under an edit of the first: never looked at.
	if err := e.q.SetPhotoParent(ctx, db.SetPhotoParentParams{ID: e.mustAsset(nodes[2]).ID,
		ParentID: e.mustAsset(nodes[0]).ID, Role: pgtype.Text{String: "original", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.SetFacesEnabled(ctx, db.SetFacesEnabledParams{UserID: e.uid, FacesEnabled: false}); err != nil {
		t.Fatal(err)
	}
	go f.Run(ctx)
	f.Kick()
	time.Sleep(500 * time.Millisecond)
	if n := ml.calls.Load(); n > 1 { // a ping at most
		t.Fatalf("%d calls with people off", n)
	}
	if _, err := e.q.SetFacesEnabled(ctx, db.SetFacesEnabledParams{UserID: e.uid, FacesEnabled: true}); err != nil {
		t.Fatal(err)
	}
	f.Kick()
	waitFor(t, "first", func() bool { return e.mustAsset(nodes[0]).FaceState.String == "done" })
	waitFor(t, "second", func() bool { return e.mustAsset(nodes[1]).FaceState.String == "done" })
	time.Sleep(300 * time.Millisecond)
	if a := e.mustAsset(nodes[2]); a.FaceState.Valid {
		t.Fatalf("original under an edit looked at: %v", a.FaceState)
	}
}

func TestFacesQueuePoisonPictureFails(t *testing.T) {
	// The service is up but always fails on one picture: that card becomes failed after a
	// few tries, the others go on, and the service is not reported down.
	e, ml, f, nodes, _ := facesEnv(t)
	ml.poison.Store(true)
	f.retry = time.Hour // a pause would stall the test: there must be none
	go f.Run(context.Background())
	f.Kick()
	for _, n := range nodes {
		waitFor(t, "every card settled", func() bool { return e.mustAsset(n).FaceState.Valid })
	}
	failed := 0
	for _, n := range nodes {
		if e.mustAsset(n).FaceState.String == "failed" {
			failed++
		}
	}
	if failed != 1 || !f.Online() {
		t.Fatalf("failed %d, online %v", failed, f.Online())
	}
}

func TestFacesQueueModelChangeRedetects(t *testing.T) {
	// The server starts with another face model: every card is looked at again (vectors of two
	// models do not compare), and the persons stay (the boxes are the same).
	e, ml, f, nodes, _ := facesEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	go f.Run(ctx)
	f.Kick()
	waitFor(t, "a person", func() bool { return len(e.people()) == 1 })
	p := e.people()[0]
	if _, err := e.q.UpdatePhotoPerson(context.Background(), db.UpdatePhotoPersonParams{ID: p.ID, UserID: e.uid,
		Name: pgtype.Text{String: "Аня", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if a := e.mustAsset(nodes[0]); a.FaceModel.String != "buffalo_l" {
		t.Fatalf("model not recorded: %v", a.FaceModel)
	}
	cancel()

	before := ml.calls.Load()
	f2 := NewFaces(e.q, f.th, &MLClient{URL: ml.srv.URL, Model: "auraface", MinScore: 0.7},
		faces.Params{MaxDistance: 0.5, MinFaces: 3}, 1)
	f2.retry = 100 * time.Millisecond
	go f2.Run(context.Background())
	f2.Kick()
	for _, n := range nodes {
		waitFor(t, "looked at with the new model", func() bool { return e.mustAsset(n).FaceModel.String == "auraface" })
	}
	if ml.calls.Load()-before < int32(len(nodes)) {
		t.Fatalf("only %d new calls", ml.calls.Load()-before)
	}
	waitFor(t, "the person kept", func() bool {
		got := e.people()
		return len(got) == 1 && got[0].ID == p.ID && got[0].Name == "Аня" && got[0].Count == 3
	})
}

func TestFacesQueueWrongModelPauses(t *testing.T) {
	// A wrong PHOTOS_ML_MODEL must not fail the library card by card: the queue pauses.
	e, ml, f, nodes, _ := facesEnv(t)
	ml.wrongModel.Store(true)
	go f.Run(context.Background())
	f.Kick()
	waitFor(t, "offline", func() bool { return !f.Online() })
	time.Sleep(500 * time.Millisecond)
	for _, n := range nodes {
		if a := e.mustAsset(n); a.FaceState.Valid {
			t.Fatalf("card %v with a wrong model setting", a.FaceState)
		}
	}
	ml.wrongModel.Store(false)
	for _, n := range nodes {
		waitFor(t, "done once the model is right", func() bool { return e.mustAsset(n).FaceState.String == "done" })
	}
}

func TestCarryPerson(t *testing.T) {
	p := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	old := []db.PhotoFace{{X1: 0.40, Y1: 0.40, X2: 0.60, Y2: 0.60, PersonID: p}}
	cases := []struct {
		name string
		d    DetectedFace
		want bool
	}{
		{"same box", DetectedFace{X1: 0.40, Y1: 0.40, X2: 0.60, Y2: 0.60}, true},
		// another detector draws the box tighter: IoU 0.36, the centre inside the old box
		{"tighter box of another detector", DetectedFace{X1: 0.44, Y1: 0.44, X2: 0.56, Y2: 0.56}, true},
		{"a face next to it", DetectedFace{X1: 0.58, Y1: 0.40, X2: 0.78, Y2: 0.60}, false},
		{"far", DetectedFace{X1: 0.0, Y1: 0.0, X2: 0.1, Y2: 0.1}, false},
	}
	for _, c := range cases {
		got := carryPerson(c.d, old)
		if got.Valid != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	// one old face goes to one new face only: the closer of two
	twoNew := []DetectedFace{{X1: 0.44, Y1: 0.44, X2: 0.56, Y2: 0.56}, {X1: 0.40, Y1: 0.40, X2: 0.60, Y2: 0.60}}
	got := carryPersons(twoNew, old)
	if got[0].Valid || !got[1].Valid {
		t.Fatalf("one to one: %v", got)
	}
}

func TestFacesQueueOfflineAtStartRecovers(t *testing.T) {
	// The service was starting when the server asked: once it answers, the flag clears by
	// itself, even with nothing in the queue (the settings must not say "unavailable" for good).
	e, ml, f, _, _ := facesEnv(t)
	if _, err := e.q.SetFacesEnabled(context.Background(), db.SetFacesEnabledParams{UserID: e.uid, FacesEnabled: false}); err != nil {
		t.Fatal(err)
	}
	ml.status.Store(503)
	f.idle = 100 * time.Millisecond
	go f.Run(context.Background())
	waitFor(t, "offline", func() bool { return !f.Online() })
	ml.status.Store(200)
	waitFor(t, "online again with an empty queue", func() bool { return f.Online() })
}
