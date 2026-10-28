package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/photos"
	"discodrive/internal/photos/maps"
	"discodrive/internal/photos/media"
	"discodrive/internal/storage"
)

type photosEnv struct {
	t     *testing.T
	ctx   context.Context
	q     *db.Queries
	root  string
	h     http.Handler
	svc   *auth.Service
	tok   string
	user  db.User
	tok2  string
	user2 db.User
	seq   int
	pool  *pgxpool.Pool
	maps  *maps.Manager
}

// newPhotosEnv starts a database and a router. With withPhotos the gallery services are
// real (skipped when the media tools are missing); workers is the thumbnail worker count.
func newPhotosEnv(t *testing.T, withPhotos bool, workers int) *photosEnv {
	t.Helper()
	ctx := context.Background()
	var tools media.Tools
	if withPhotos {
		if tools = media.Detect(); !tools.Available() {
			t.Skip("media tools not installed")
		}
	}
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("Docker required: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, _ := pgxpool.New(ctx, dsn)
	t.Cleanup(pool.Close)
	q := db.New(pool)
	svc := auth.NewService(pool, auth.NewTokenIssuer("secret", time.Hour), nil)
	e := &photosEnv{t: t, ctx: ctx, q: q, root: t.TempDir(), svc: svc, pool: pool}
	if e.tok, e.user, err = svc.Register(ctx, "a@x.test", "password12"); err != nil {
		t.Fatal(err)
	}
	if e.tok2, e.user2, err = svc.Register(ctx, "b@x.test", "password12"); err != nil {
		t.Fatal(err)
	}
	var idx *photos.Indexer
	var th *photos.Thumbs
	if withPhotos {
		exif, err := media.NewExif(tools)
		if err != nil {
			t.Skipf("exiftool: %v", err)
		}
		t.Cleanup(func() { _ = exif.Close() })
		idx = photos.NewIndexer(q, e.root, exif, tinyGeo{})
		th = photos.NewThumbs(q, e.root, tools, workers, exif)
	}
	for _, u := range []db.User{e.user, e.user2} {
		if !withPhotos {
			break
		}
		// rows are inserted directly; the gallery is on
		if _, err := q.UpsertPhotoSettings(ctx, db.UpsertPhotoSettingsParams{UserID: u.ID, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	files := storage.NewFileService(pool, storage.NewLocalDisk(e.root))
	e.maps = maps.New(ctx, q, e.root, maps.Config{})
	t.Cleanup(e.maps.Wait)
	e.h = NewRouter(svc, q, files, nil, e.root, nil, nil, nil, nil, nil, nil, nil, false, nil, nil, nil, nil, nil, nil, nil, nil, idx, th, e.maps)
	return e
}

func (e *photosEnv) do(tok, method, path string, body any, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *photosEnv) json(rec *httptest.ResponseRecorder, want int, out any) {
	e.t.Helper()
	if rec.Code != want {
		e.t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			e.t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
	}
}

func (e *photosEnv) dir(u db.User, parent *db.Node, name string) db.Node {
	e.t.Helper()
	rel := db.UUIDString(u.ID) + "/" + name
	p := pgtype.UUID{}
	if parent != nil {
		rel = parent.DiskPath.String + "/" + name
		p = parent.ID
	}
	if err := os.MkdirAll(filepath.Join(e.root, rel), 0o755); err != nil {
		e.t.Fatal(err)
	}
	n, err := e.q.CreateNode(e.ctx, db.CreateNodeParams{UserID: u.ID, ParentID: p, Name: name, IsDir: true, DiskPath: pgtype.Text{String: rel, Valid: true}})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

type assetOpts struct {
	kind   string
	codec  string
	taken  time.Time
	hidden bool
	trash  bool
	place  int32 // a tinyGeo place id
	jpeg   bool  // write a real JPEG file
}

// asset creates a node (and file) plus its asset row for u.
func (e *photosEnv) asset(u db.User, o assetOpts) db.PhotoAsset {
	e.t.Helper()
	e.seq++
	if o.kind == "" {
		o.kind = "photo"
	}
	name := "f" + strings.Repeat("x", e.seq) + ".jpg"
	rel := db.UUIDString(u.ID) + "/" + name
	_ = os.MkdirAll(filepath.Join(e.root, db.UUIDString(u.ID)), 0o755)
	var content []byte
	if o.jpeg {
		img := image.NewRGBA(image.Rect(0, 0, 64, 48))
		for x := 0; x < 64; x++ {
			for y := 0; y < 48; y++ {
				img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 5), 120, 255})
			}
		}
		var buf bytes.Buffer
		_ = jpeg.Encode(&buf, img, nil)
		content = buf.Bytes()
	} else {
		content = []byte("x")
	}
	if err := os.WriteFile(filepath.Join(e.root, rel), content, 0o644); err != nil {
		e.t.Fatal(err)
	}
	n, err := e.q.CreateNode(e.ctx, db.CreateNodeParams{UserID: u.ID, Name: name, Size: pgtype.Int8{Int64: int64(len(content)), Valid: true}, DiskPath: pgtype.Text{String: rel, Valid: true}})
	if err != nil {
		e.t.Fatal(err)
	}
	if o.taken.IsZero() {
		o.taken = time.Date(2019, 7, 4, 12, 0, 0, 0, time.UTC)
	}
	a, err := e.q.UpsertPhotoAsset(e.ctx, db.UpsertPhotoAssetParams{
		UserID: u.ID, NodeID: n.ID, Kind: o.kind,
		TakenAt:    pgtype.Timestamptz{Time: o.taken, Valid: true},
		TakenLocal: pgtype.Timestamp{Time: o.taken, Valid: true},
		DateSource: "exif", Width: pgtype.Int4{Int32: 64, Valid: true}, Height: pgtype.Int4{Int32: 48, Valid: true},
		VideoCodec:  pgtype.Text{String: o.codec, Valid: o.codec != ""},
		PlaceID:     pgtype.Int4{Int32: o.place, Valid: o.place != 0},
		CountryCode: pgtype.Text{String: "KG", Valid: o.place != 0},
		NodeVersion: pgtype.Int8{Int64: n.Version, Valid: true},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if o.hidden {
		hid := true
		if err := e.q.SetPhotoFlags(e.ctx, db.SetPhotoFlagsParams{UserID: u.ID, Ids: []pgtype.UUID{a.ID}, Hidden: pgtype.Bool{Bool: hid, Valid: true}}); err != nil {
			e.t.Fatal(err)
		}
	}
	if o.trash {
		e.q.TrashPhotoAssetByNode(e.ctx, n.ID)
	}
	return a
}

func TestPhotosSettings_LiteUnavailable(t *testing.T) {
	e := newPhotosEnv(t, false, 0)
	var s photoSettingsResponse
	e.json(e.do(e.tok, "GET", "/me/photos/v1/settings", nil), 200, &s)
	if s.Available || s.Enabled || len(s.Sources) != 0 {
		t.Fatalf("settings = %+v", s)
	}
	for _, p := range []string{"/buckets", "/months/2019-07", "/on-this-day", "/assets/" + db.UUIDString(e.user.ID), "/assets/x/thumb"} {
		rec := e.do(e.tok, "GET", "/me/photos/v1"+p, nil)
		var out map[string]string
		e.json(rec, 409, &out)
		if out["error"] != "photos_unavailable" {
			t.Fatalf("%s: %v", p, out)
		}
	}
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/settings", map[string]any{"enabled": true, "sourceIds": []string{}}), 409, nil)
	e.json(e.do(e.tok, "POST", "/me/photos/v1/assets/x/play", map[string]any{}), 409, nil)
	e.json(e.do("", "GET", "/me/photos/v1/assets/x/video?t=bad", nil), 409, nil)
}

func TestPhotosSettings_NestedSourcesCollapse(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	a := e.dir(e.user, nil, "A")
	b := e.dir(e.user, &a, "B")
	c := e.dir(e.user, nil, "C")
	foreign := e.dir(e.user2, nil, "Theirs")

	id := db.UUIDString
	var s photoSettingsResponse
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/settings", map[string]any{"enabled": true, "sourceIds": []string{id(b.ID), id(a.ID), id(c.ID), id(a.ID)}}), 200, &s)
	if !s.Available || !s.Enabled || len(s.Sources) != 2 {
		t.Fatalf("settings = %+v", s)
	}
	got := map[string]bool{}
	for _, src := range s.Sources {
		got[src.ID] = true
	}
	if !got[id(a.ID)] || !got[id(c.ID)] {
		t.Fatalf("sources = %+v", s.Sources)
	}
	// Someone else's folder, unshared, is not found.
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/settings", map[string]any{"enabled": true, "sourceIds": []string{id(foreign.ID)}}), 404, nil)
	// Shared to the user: stage 1 takes only own folders, so it is rejected as well.
	if _, err := e.pool.Exec(e.ctx, `INSERT INTO resource_shares (resource_type, resource_id, owner_id, shared_with_user, access) VALUES ('file_node', $1, $2, $3, 'read')`, foreign.ID, e.user2.ID, e.user.ID); err != nil {
		t.Fatalf("cannot create share: %v", err)
	}
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/settings", map[string]any{"enabled": true, "sourceIds": []string{id(foreign.ID)}}), 404, nil)
	for _, src := range s.Sources {
		if strings.Contains(src.Path, db.UUIDString(e.user.ID)) || src.Path != src.Name {
			t.Fatalf("path leaks or is not user-relative: %+v", src)
		}
	}
}

func TestPhotosSettings_RemovingSourcesDropsFeed(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	src := e.dir(e.user, nil, "Pics")
	// A real JPEG inside the source, indexed by the pass the PUT starts.
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	rel := src.DiskPath.String + "/p.jpg"
	_ = os.WriteFile(filepath.Join(e.root, rel), buf.Bytes(), 0o644)
	if _, err := e.q.CreateNode(e.ctx, db.CreateNodeParams{UserID: e.user.ID, ParentID: src.ID, Name: "p.jpg", Size: pgtype.Int8{Int64: int64(buf.Len()), Valid: true}, DiskPath: pgtype.Text{String: rel, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	buckets := func() int {
		var b []map[string]any
		e.json(e.do(e.tok, "GET", "/me/photos/v1/buckets", nil), 200, &b)
		return len(b)
	}
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/settings", map[string]any{"enabled": true, "sourceIds": []string{db.UUIDString(src.ID)}}), 200, nil)
	waitPhotos(t, func() bool { return buckets() == 1 })
	// Disabled: empty feeds, 200.
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/settings", map[string]any{"enabled": false, "sourceIds": []string{db.UUIDString(src.ID)}}), 200, nil)
	if buckets() != 0 {
		t.Fatal("disabled gallery must show an empty feed")
	}
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/settings", map[string]any{"enabled": true, "sourceIds": []string{db.UUIDString(src.ID)}}), 200, nil)
	waitPhotos(t, func() bool { return buckets() == 1 })
	// No sources left: the pass prunes the user's assets.
	e.json(e.do(e.tok, "PUT", "/me/photos/v1/settings", map[string]any{"enabled": true, "sourceIds": []string{}}), 200, nil)
	waitPhotos(t, func() bool { return buckets() == 0 })
	var n int
	_ = e.pool.QueryRow(e.ctx, `SELECT count(*) FROM photo_assets WHERE user_id = $1`, e.user.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d asset rows remain", n)
	}
}

func waitPhotos(t *testing.T, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}

func TestPhotosTimeline_BucketsAndMonth(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	jul := time.Date(2019, 7, 4, 12, 0, 0, 0, time.UTC)
	e.asset(e.user, assetOpts{taken: jul, place: 1})
	e.asset(e.user, assetOpts{taken: jul.Add(time.Hour), kind: "video", codec: "h264"})
	e.asset(e.user, assetOpts{taken: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)})
	e.asset(e.user, assetOpts{taken: jul, hidden: true})
	e.asset(e.user, assetOpts{taken: jul, trash: true})
	e.asset(e.user2, assetOpts{taken: jul})

	var buckets []struct {
		Month string `json:"month"`
		Count int    `json:"count"`
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/buckets", nil), 200, &buckets)
	if len(buckets) != 2 || buckets[0].Month != "2020-01" || buckets[0].Count != 1 || buckets[1].Month != "2019-07" || buckets[1].Count != 2 {
		t.Fatalf("buckets = %+v", buckets)
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/buckets?kind=video", nil), 200, &buckets)
	if len(buckets) != 1 || buckets[0].Count != 1 {
		t.Fatalf("video buckets = %+v", buckets)
	}

	var rows []photoRowDTO
	e.json(e.do(e.tok, "GET", "/me/photos/v1/months/2019-07", nil), 200, &rows)
	if len(rows) != 2 || rows[0].Kind != "video" || rows[1].Kind != "photo" {
		t.Fatalf("month rows = %+v", rows)
	}
	if rows[1].TakenAt != "2019-07-04T12:00:00Z" || rows[1].Thumb == "" || rows[1].Place == nil || *rows[1].Place != "Bishkek" {
		t.Fatalf("row = %+v", rows[1])
	}
	// Place names follow the language the client asks in.
	e.json(e.do(e.tok, "GET", "/me/photos/v1/months/2019-07", nil, "Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8"), 200, &rows)
	if rows[1].Place == nil || *rows[1].Place != "Бишкек" || rows[0].Place != nil {
		t.Fatalf("ru rows = %+v", rows)
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/months/nope", nil), 400, nil)
}

func TestPhotosAsset_ForeignIs404(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	mine := e.asset(e.user, assetOpts{place: 2})
	hidden := e.asset(e.user, assetOpts{hidden: true})
	trashed := e.asset(e.user, assetOpts{trash: true})

	var card map[string]any
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(mine.ID), nil), 200, &card)
	if card["place"] != "Osh" || card["country"] != "KG" || card["file"].(map[string]any)["nodeId"] != db.UUIDString(mine.NodeID) {
		t.Fatalf("card = %v", card)
	}
	// A file in the root has no folder to open.
	if v, ok := card["file"].(map[string]any)["folderId"]; !ok || v != nil {
		t.Fatalf("folderId = %v (present %v)", v, ok)
	}
	for name, id := range map[string]pgtype.UUID{"mine as foreign": mine.ID, "hidden": hidden.ID, "trashed": trashed.ID} {
		tok := e.tok
		if name == "mine as foreign" {
			tok = e.tok2
		}
		for _, suffix := range []string{"", "/thumb", "/preview"} {
			if rec := e.do(tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(id)+suffix, nil); rec.Code != 404 {
				t.Fatalf("%s%s = %d", name, suffix, rec.Code)
			}
		}
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/not-a-uuid", nil), 404, nil)
}

func TestPhotosThumb_CacheHeaders(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	a := e.asset(e.user, assetOpts{jpeg: true})
	url := "/me/photos/v1/assets/" + db.UUIDString(a.ID) + "/thumb"
	rec := e.do(e.tok, "GET", url, nil)
	if rec.Code != 200 {
		t.Fatalf("thumb = %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
		t.Fatalf("cache-control %q", cc)
	}
	etag := rec.Header().Get("ETag")
	var rows []photoRowDTO
	e.json(e.do(e.tok, "GET", "/me/photos/v1/months/2019-07", nil), 200, &rows)
	if etag != `"`+rows[0].Thumb+`"` {
		t.Fatalf("etag %q vs row thumb %q", etag, rows[0].Thumb)
	}
	if rec2 := e.do(e.tok, "GET", url, nil, "If-None-Match", etag); rec2.Code != 304 {
		t.Fatalf("conditional = %d", rec2.Code)
	}
	if rec3 := e.do(e.tok, "GET", strings.Replace(url, "thumb", "preview", 1), nil); rec3.Code != 200 || rec3.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("preview = %d", rec3.Code)
	}
	// A file that cannot be rendered is 404 render_failed.
	bad := e.asset(e.user, assetOpts{})
	var out map[string]string
	e.json(e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(bad.ID)+"/thumb", nil), 404, &out)
	if out["error"] != "render_failed" {
		t.Fatalf("bad thumb: %v", out)
	}
}

func TestPhotosPlay_H264DirectHEVCQueued(t *testing.T) {
	e := newPhotosEnv(t, true, 1)
	h264 := e.asset(e.user, assetOpts{kind: "video", codec: "h264"})
	hevc := e.asset(e.user, assetOpts{kind: "video", codec: "hevc"})
	play := func(id pgtype.UUID, body any) map[string]any {
		var out map[string]any
		e.json(e.do(e.tok, "POST", "/me/photos/v1/assets/"+db.UUIDString(id)+"/play", body), 200, &out)
		return out
	}
	out := play(h264.ID, map[string]any{"hevc": false})
	if out["ready"] != true || !strings.HasPrefix(out["url"].(string), "/files/"+db.UUIDString(h264.NodeID)+"/stream?t=") {
		t.Fatalf("h264 = %v", out)
	}
	out = play(hevc.ID, map[string]any{"hevc": true})
	if out["ready"] != true || !strings.Contains(out["url"].(string), "/stream?t=") {
		t.Fatalf("hevc+client = %v", out)
	}
	out = play(hevc.ID, map[string]any{"hevc": false})
	if out["ready"] != false || out["url"] != nil || out["unsupported"] != nil {
		t.Fatalf("hevc queued = %v", out)
	}
	got, _ := e.q.GetPhotoAsset(e.ctx, db.GetPhotoAssetParams{ID: hevc.ID, UserID: e.user.ID})
	if got.TranscodeState.String != "pending" {
		t.Fatalf("transcode_state = %q", got.TranscodeState.String)
	}

	// The finished copy is served by the token endpoint, with ranges.
	if err := e.q.SetTranscodeState(e.ctx, db.SetTranscodeStateParams{ID: hevc.ID, TranscodeState: pgtype.Text{String: "ready", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	dst := photos.AssetDerivPath(e.root, got, "h264")
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	_ = os.WriteFile(dst, []byte("0123456789"), 0o644)
	out = play(hevc.ID, map[string]any{"hevc": false})
	if out["ready"] != true || !strings.HasPrefix(out["url"].(string), "/me/photos/v1/assets/"+db.UUIDString(hevc.ID)+"/video?t=") {
		t.Fatalf("ready = %v", out)
	}
	rec := e.do("", "GET", out["url"].(string), nil, "Range", "bytes=2-4")
	if rec.Code != 206 || rec.Body.String() != "234" {
		t.Fatalf("video range = %d %q", rec.Code, rec.Body.String())
	}
	// A token for another node does not open it.
	if rec := e.do("", "GET", "/me/photos/v1/assets/"+db.UUIDString(hevc.ID)+"/video?t=junk", nil); rec.Code != 401 {
		t.Fatalf("junk token = %d", rec.Code)
	}

	// Economy mode: no one to transcode.
	eco := newPhotosEnv(t, true, 0)
	v := eco.asset(eco.user, assetOpts{kind: "video", codec: "hevc"})
	var eout map[string]any
	eco.json(eco.do(eco.tok, "POST", "/me/photos/v1/assets/"+db.UUIDString(v.ID)+"/play", map[string]any{"hevc": false}), 200, &eout)
	if eout["unsupported"] != true || eout["ready"] != false || eout["url"] != nil {
		t.Fatalf("economy = %v", eout)
	}
}

func TestPhotosOnThisDay_PastYearsOnly(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	now := time.Now()
	e.asset(e.user, assetOpts{taken: time.Date(2019, 3, 15, 10, 0, 0, 0, time.UTC)})
	e.asset(e.user, assetOpts{taken: time.Date(2021, 3, 15, 10, 0, 0, 0, time.UTC)})
	e.asset(e.user, assetOpts{taken: time.Date(2021, 3, 15, 11, 0, 0, 0, time.UTC), hidden: true})
	e.asset(e.user, assetOpts{taken: time.Date(now.Year(), 3, 15, 10, 0, 0, 0, time.UTC)})
	e.asset(e.user, assetOpts{taken: time.Date(2020, 3, 16, 10, 0, 0, 0, time.UTC)})
	e.asset(e.user2, assetOpts{taken: time.Date(2018, 3, 15, 10, 0, 0, 0, time.UTC)})

	var groups []struct {
		Year   int           `json:"year"`
		Assets []photoRowDTO `json:"assets"`
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/on-this-day?date=03-15", nil), 200, &groups)
	if len(groups) != 2 || groups[0].Year != 2021 || groups[1].Year != 2019 || len(groups[0].Assets) != 1 || len(groups[1].Assets) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/on-this-day?date=13-45", nil), 400, nil)
}

// Files that could not be processed are listed with their folder and error, a page at a
// time, without hidden ones and without other users' files; the settings count them.
func TestPhotosFailedFiles(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	thumbFail := e.asset(e.user, assetOpts{})
	metaFail := e.asset(e.user, assetOpts{})
	hidden := e.asset(e.user, assetOpts{hidden: true})
	ok := e.asset(e.user, assetOpts{})
	foreign := e.asset(e.user2, assetOpts{})
	for _, a := range []db.PhotoAsset{thumbFail, hidden, foreign} {
		if err := e.q.SetThumbState(e.ctx, db.SetThumbStateParams{ID: a.ID, ThumbState: "failed",
			ThumbError: pgtype.Text{String: "vips: bad file", Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET index_error = 'exiftool timed out' WHERE id = $1`, metaFail.ID); err != nil {
		t.Fatal(err)
	}
	_ = ok

	var page struct {
		Items []struct{ ID, Name, Folder, Error string }
		More  bool
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/failed", nil), 200, &page)
	if len(page.Items) != 2 || page.More {
		t.Fatalf("failed files %+v", page)
	}
	errs := map[string]string{}
	for _, it := range page.Items {
		errs[it.ID] = it.Error
		if it.Folder != "/" || !strings.HasSuffix(it.Name, ".jpg") {
			t.Fatalf("item %+v", it)
		}
	}
	if errs[db.UUIDString(thumbFail.ID)] != "vips: bad file" || errs[db.UUIDString(metaFail.ID)] != "exiftool timed out" {
		t.Fatalf("errors %v", errs)
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/failed?limit=1", nil), 200, &page)
	if len(page.Items) != 1 || !page.More {
		t.Fatalf("first page %+v", page)
	}
	first := page.Items[0].ID
	e.json(e.do(e.tok, "GET", "/me/photos/v1/failed?limit=1&offset=1", nil), 200, &page)
	if len(page.Items) != 1 || page.More || page.Items[0].ID == first {
		t.Fatalf("second page %+v", page)
	}

	var st struct {
		Indexing struct {
			Total, Ready, Failed, Pending, FailedFiles int64
			Scanning                                   bool
		}
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/settings", nil), 200, &st)
	if st.Indexing.FailedFiles != 2 || st.Indexing.Failed != 2 || st.Indexing.Total != 4 || st.Indexing.Scanning {
		t.Fatalf("indexing %+v", st.Indexing)
	}

	lite := newPhotosEnv(t, false, 0)
	if rec := lite.do(lite.tok, "GET", "/me/photos/v1/failed", nil); rec.Code != 409 {
		t.Fatalf("lite: %d", rec.Code)
	}
}
