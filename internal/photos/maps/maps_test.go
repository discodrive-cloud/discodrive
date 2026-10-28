package maps

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

func testQueries(t *testing.T) *db.Queries {
	t.Helper()
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("need Docker: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return db.New(pool)
}

func assetsZip(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range map[string]string{
		"basemaps-assets-abc/fonts/Noto Sans Regular/0-255.pbf": "glyphs",
		"basemaps-assets-abc/sprites/v4/light.json":             "{}",
		"basemaps-assets-abc/README.md":                         "skipped",
		"basemaps-assets-abc/fonts/../../evil.txt":              "evil",
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return b.Bytes()
}

func zipOf(t *testing.T, name, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create(name)
	_, _ = w.Write([]byte(body))
	_ = zw.Close()
	return b.Bytes()
}

func citiesZip(t *testing.T) []byte {
	return zipOf(t, "cities500.txt", "1\tBishkek\tBishkek\t\t42.87\t74.59\tP\tPPL\tKG\n")
}

func altZip(t *testing.T) []byte {
	return zipOf(t, "alternateNamesV2.txt", "5\t1\tru\t\u0411\u0438\u0448\u043a\u0435\u043a\t1\t\t\t\n")
}

// upstream stands in for protomaps, GeoNames and GitHub at once. Only yesterday's build
// exists.
func upstream(t *testing.T) *httptest.Server {
	t.Helper()
	yday := time.Now().UTC().AddDate(0, 0, -1).Format("20060102")
	zipBody := assetsZip(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/builds/"+yday+".pmtiles":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/geo/cities500.zip":
			_, _ = w.Write(citiesZip(t))
		case r.URL.Path == "/geo/alternateNamesV2.zip":
			_, _ = w.Write(altZip(t))
		case strings.HasPrefix(r.URL.Path, "/geo/"):
			_, _ = w.Write([]byte("geo " + r.URL.Path))
		case r.URL.Path == "/assets.zip":
			_, _ = w.Write(zipBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fakeExtract struct {
	calls   atomic.Int32
	gate    chan struct{} // nil: do not wait
	fail    error
	lastSrc string
	bbox    string
	maxzoom int8
	size    int  // bytes written (default: a few)
	stall   bool // write, then hang until cancelled
}

func (f *fakeExtract) run(ctx context.Context, src, bbox string, maxzoom int8, out string) error {
	f.calls.Add(1)
	f.lastSrc, f.bbox, f.maxzoom = src, bbox, maxzoom
	body := []byte("PMTiles-fake-0123456789")
	if f.size > 0 {
		body = bytes.Repeat([]byte("x"), f.size)
	}
	if err := os.WriteFile(out, body, 0o600); err != nil {
		return err
	}
	if f.stall {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.fail
}

func newMgr(t *testing.T, q *db.Queries, fx *fakeExtract, mod ...func(*Config)) (*Manager, *httptest.Server) {
	t.Helper()
	up := upstream(t)
	ctx, cancel := context.WithCancel(context.Background())
	cfg := Config{BuildsURL: up.URL + "/builds", GeoURL: up.URL + "/geo",
		AssetsZipURL: up.URL + "/assets.zip", Extract: fx.run,
		Free: func(string) (uint64, error) { return 1 << 50, nil }, Tick: 20 * time.Millisecond}
	for _, f := range mod {
		f(&cfg)
	}
	m := New(ctx, q, t.TempDir(), cfg)
	t.Cleanup(func() { cancel(); m.Wait() })
	return m, up
}

func waitState(t *testing.T, m *Manager, want string) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := m.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %q, want %q (%+v)", st.State, want, st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMaps_DownloadStateMachine(t *testing.T) {
	ctx := context.Background()
	q := testQueries(t)
	fx := &fakeExtract{gate: make(chan struct{})}
	m, _ := newMgr(t, q, fx)
	var geoCalls atomic.Int32
	m.OnGeo = func() { geoCalls.Add(1) }

	if st, _ := m.Status(ctx); st.State != StateNone || m.Ready(ctx) {
		t.Fatalf("initial %+v", st)
	}
	if err := m.Set(ctx, "atlantis"); !errors.Is(err, ErrUnknownRegion) {
		t.Fatalf("unknown region: %v", err)
	}
	if err := m.Set(ctx, "centralasia"); err != nil {
		t.Fatal(err)
	}
	if err := m.Set(ctx, "centralasia"); err != nil { // idempotent while running
		t.Fatal(err)
	}
	st := waitState(t, m, StateDownloading)
	if st.Region != "centralasia" || m.Ready(ctx) {
		t.Fatalf("downloading %+v", st)
	}
	close(fx.gate)
	m.Wait()
	st = waitState(t, m, StateReady)
	if st.Bytes != int64(len("PMTiles-fake-0123456789")) || !m.Ready(ctx) {
		t.Fatalf("ready %+v", st)
	}
	if fx.calls.Load() != 1 || fx.bbox != "46,35,88,56" || fx.maxzoom != 14 ||
		!strings.HasSuffix(fx.lastSrc, time.Now().UTC().AddDate(0, 0, -1).Format("20060102")+".pmtiles") {
		t.Fatalf("extract args: calls=%d %q %q %d", fx.calls.Load(), fx.lastSrc, fx.bbox, fx.maxzoom)
	}
	if geoCalls.Load() != 1 {
		t.Fatalf("OnGeo called %d times", geoCalls.Load())
	}
	if b, err := os.ReadFile(filepath.Join(m.GeoDir(), "place-names.tsv")); err != nil || string(b) != "1\tru\t\u0411\u0438\u0448\u043a\u0435\u043a\n" {
		t.Errorf("place-names.tsv: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(m.GeoDir(), "alternateNamesV2.zip.part")); err == nil {
		t.Error("big alternate-names zip kept")
	}
	for _, p := range []string{m.RegionFile(), filepath.Join(m.GeoDir(), "cities500.zip"),
		filepath.Join(m.AssetsDir(), "fonts", "Noto Sans Regular", "0-255.pbf"), filepath.Join(m.AssetsDir(), "sprites", "v4", "light.json")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(m.AssetsDir(), "README.md")); err == nil {
		t.Error("README should not be unpacked")
	}
	if _, err := os.Stat(m.RegionFile() + ".part"); err == nil {
		t.Error(".part left behind")
	}
	// Ready + same region: nothing runs again.
	if err := m.Set(ctx, "centralasia"); err != nil || fx.calls.Load() != 1 {
		t.Fatalf("idempotent ready: %v calls=%d", err, fx.calls.Load())
	}
	// Another region replaces it; a download in flight is cancelled by the next choice.
	fx.gate = make(chan struct{})
	if err := m.Set(ctx, "europe"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StateDownloading)
	if err := m.Set(ctx, "world"); err != nil {
		t.Fatal(err)
	}
	close(fx.gate)
	m.Wait()
	if st = waitState(t, m, StateReady); st.Region != "world" || fx.maxzoom != 10 || fx.bbox != "" {
		t.Fatalf("after switch %+v maxzoom=%d bbox=%q", st, fx.maxzoom, fx.bbox)
	}
	// Removing the region removes the files and the state.
	if err := m.Set(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.Status(ctx); st.State != StateNone || st.Region != "" || m.Ready(ctx) {
		t.Fatalf("after clear %+v", st)
	}
	if _, err := os.Stat(m.RegionFile()); err == nil {
		t.Error("tiles kept after clear")
	}
}

func TestMaps_DownloadFailureAndResume(t *testing.T) {
	ctx := context.Background()
	q := testQueries(t)
	fx := &fakeExtract{fail: errors.New("boom")}
	m, _ := newMgr(t, q, fx)
	if err := m.Set(ctx, "europe"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	st := waitState(t, m, StateFailed)
	if !strings.Contains(st.Error, "boom") || m.Ready(ctx) {
		t.Fatalf("failed %+v", st)
	}
	if _, err := os.Stat(m.RegionFile() + ".part"); err == nil {
		t.Error(".part kept after failure")
	}
	// A retry of the same region runs again and can succeed.
	fx.fail = nil
	if err := m.Set(ctx, "europe"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	waitState(t, m, StateReady)

	// A restart in the middle of a download: the persisted "downloading" state is
	// picked up and the download starts again.
	m.save(ctx, "europe", persisted{State: StateDownloading})
	_ = os.Remove(m.RegionFile())
	before := fx.calls.Load()
	m2 := New(ctx, q, filepath.Dir(filepath.Dir(m.dir)), m.cfg)
	t.Cleanup(m2.Wait)
	m2.Resume(ctx)
	m2.Wait()
	waitState(t, m2, StateReady)
	if fx.calls.Load() != before+1 {
		t.Fatalf("resume did not restart the download")
	}
}

func TestMaps_ServeRange(t *testing.T) {
	m := New(context.Background(), nil, t.TempDir(), Config{})
	if err := os.MkdirAll(m.dir, 0o750); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	m.ServeRegion(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no file: %d", rec.Code)
	}
	if err := os.WriteFile(m.RegionFile(), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec = httptest.NewRecorder()
	m.ServeRegion(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" ||
		rec.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("range: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	rec = httptest.NewRecorder()
	m.ServeRegion(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 200 || rec.Body.Len() != 10 || rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("full: %d %v", rec.Code, rec.Header())
	}
}

func TestMaps_AssetsPathTraversal(t *testing.T) {
	m := New(context.Background(), nil, t.TempDir(), Config{})
	if err := os.MkdirAll(filepath.Join(m.AssetsDir(), "fonts", "A B"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.AssetsDir(), "fonts", "A B", "0-255.pbf"), []byte("pbf"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(m.dir, "secret.txt") // beside assets/, outside it
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(m.AssetsDir(), "link.txt")); err != nil {
		t.Fatal(err)
	}
	get := func(rel string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeAsset(rec, httptest.NewRequest("GET", "/x", nil), rel)
		return rec
	}
	if rec := get("fonts/A B/0-255.pbf"); rec.Code != 200 || rec.Body.String() != "pbf" || rec.Header().Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("legit: %d %q", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"../secret.txt", "fonts/../../secret.txt", "link.txt", "fonts", "", "/etc/passwd", "fonts//A B/0-255.pbf", `..\secret.txt`, "nope.pbf"} {
		if rec := get(bad); rec.Code != http.StatusNotFound {
			t.Errorf("%q: %d %q", bad, rec.Code, rec.Body.String())
		}
	}
}

func TestMaps_Unavailable(t *testing.T) {
	q := testQueries(t)
	m := New(context.Background(), q, t.TempDir(), Config{})
	if m.Available() {
		t.Fatal("available without a tool")
	}
	if st, _ := m.Status(context.Background()); st.State != StateUnavailable {
		t.Fatalf("%+v", st)
	}
	if err := m.Set(context.Background(), "europe"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Set: %v", err)
	}
}

func TestMaps_FreeSpacePreflight(t *testing.T) {
	q := testQueries(t)
	fx := &fakeExtract{}
	m, _ := newMgr(t, q, fx, func(c *Config) { c.Free = func(string) (uint64, error) { return 500 << 20, nil } })
	if err := m.Set(context.Background(), "centralasia"); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, m, StateFailed)
	if !strings.Contains(st.Error, "free disk space") || fx.calls.Load() != 0 {
		t.Fatalf("%+v calls=%d", st, fx.calls.Load())
	}
}

func TestMaps_SizeCap(t *testing.T) {
	q := testQueries(t)
	fx := &fakeExtract{size: 3000, stall: true}
	m, _ := newMgr(t, q, fx)
	m.approx["centralasia"] = 1000 // cap = 2000 < 3000 written
	if err := m.Set(context.Background(), "centralasia"); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, m, StateFailed)
	if !strings.Contains(st.Error, "twice the expected size") {
		t.Fatalf("%+v", st)
	}
	if _, err := os.Stat(m.RegionFile() + ".part"); err == nil {
		t.Error(".part kept")
	}
}

func TestMaps_ExtractStallFails(t *testing.T) {
	q := testQueries(t)
	fx := &fakeExtract{stall: true}
	m, _ := newMgr(t, q, fx, func(c *Config) { c.Stall = 150 * time.Millisecond })
	if err := m.Set(context.Background(), "centralasia"); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, m, StateFailed)
	if !strings.Contains(st.Error, "stalled") {
		t.Fatalf("%+v", st)
	}
}

func TestMaps_HTTPStallFails(t *testing.T) {
	q := testQueries(t)
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // a body that never arrives
	}))
	t.Cleanup(hang.Close)
	fx := &fakeExtract{}
	m, _ := newMgr(t, q, fx, func(c *Config) { c.GeoURL = hang.URL; c.Stall = 200 * time.Millisecond })
	if err := m.Set(context.Background(), "centralasia"); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, m, StateFailed)
	if !strings.Contains(st.Error, "stalled") || fx.calls.Load() != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestMaps_StalledSameRegionRestarts(t *testing.T) {
	ctx := context.Background()
	q := testQueries(t)
	fx := &fakeExtract{stall: true}
	m, _ := newMgr(t, q, fx, func(c *Config) { c.RestartAfter = 100 * time.Millisecond })
	if err := m.Set(ctx, "centralasia"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StateDownloading)
	for fx.calls.Load() < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	// fresh progress: the repeat is a no-op
	m.last.Store(time.Now().Add(time.Hour).UnixNano())
	if err := m.Set(ctx, "centralasia"); err != nil || fx.calls.Load() != 1 {
		t.Fatalf("repeat while progressing: %v calls=%d", err, fx.calls.Load())
	}
	// no progress for longer than RestartAfter: it is restarted
	m.last.Store(time.Now().Add(-time.Minute).UnixNano())
	if err := m.Set(ctx, "centralasia"); err != nil {
		t.Fatal(err)
	}
	for fx.calls.Load() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMaps_ConcurrentSetLastWins(t *testing.T) {
	ctx := context.Background()
	q := testQueries(t)
	fx := &fakeExtract{}
	m, _ := newMgr(t, q, fx)
	var wg sync.WaitGroup
	for _, id := range []string{"centralasia", "europe", "world", "europe", "centralasia"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Set(ctx, id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	m.Wait()
	// whichever request ran last decided: the state is consistent with one region, never mixed
	st := waitState(t, m, StateReady)
	if _, ok := FindRegion(st.Region); !ok {
		t.Fatalf("%+v", st)
	}
	if fi, err := os.Stat(m.RegionFile()); err != nil || fi.Size() == 0 {
		t.Fatalf("tiles: %v", err)
	}
	// and a clear after that really clears
	if err := m.Set(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.Status(ctx); st.State != StateNone {
		t.Fatalf("%+v", st)
	}
}

// A script standing in for the pmtiles CLI: records its arguments and writes the output.
func TestMaps_CLIExtract(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "pmtiles")
	script := "#!/bin/sh\necho \"$@\" > " + args + "\nfor last; do :; done\nprintf PMTilesX > \"$last\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.pmtiles")
	if err := cliExtract(bin)(context.Background(), "https://h/x.pmtiles", "1,2,3,4", 12, out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(args)
	want := "extract --quiet --maxzoom=12 --bbox=1,2,3,4 https://h/x.pmtiles " + out + "\n"
	if string(got) != want {
		t.Fatalf("args %q want %q", got, want)
	}
	if err := cliExtract(bin)(context.Background(), "u", "", 10, out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(args); strings.Contains(string(got), "--bbox") {
		t.Fatalf("world must not pass a bbox: %q", got)
	}
	fail := filepath.Join(dir, "fail")
	_ = os.WriteFile(fail, []byte("#!/bin/sh\necho nope >&2\nexit 3\n"), 0o755)
	if err := cliExtract(fail)(context.Background(), "u", "", 10, out); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error should carry stderr: %v", err)
	}
}
