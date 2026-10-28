package photos

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/library"
	"discodrive/internal/photos/media"
	"discodrive/internal/storage"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	q      *db.Queries
	fs     *storage.FileService
	ix     *Indexer
	tools  media.Tools
	root   string
	userID string
	uid    pgtype.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tools := media.Detect()
	if !tools.Available() {
		t.Skip("media tools not available")
	}
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
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "p@x", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	exif, err := media.NewExif(tools)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exif.Close() })
	root := t.TempDir()
	if _, err := q.UpsertPhotoSettings(ctx, db.UpsertPhotoSettingsParams{UserID: u.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, pool: pool, q: q, fs: storage.NewFileService(pool, storage.NewLocalDisk(root)),
		ix: NewIndexer(q, root, exif, nil), tools: tools, root: root, userID: db.UUIDString(u.ID), uid: u.ID}
}

func id(n db.Node) string { return db.UUIDString(n.ID) }

// source creates a top-level folder and makes it a photo source.
func (e *env) source(name string) db.Node {
	e.t.Helper()
	ctx := context.Background()
	f, err := e.fs.CreateFolder(ctx, e.userID, nil, name)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.q.InsertPhotoSource(ctx, db.InsertPhotoSourceParams{UserID: e.uid, FolderNodeID: f.ID}); err != nil {
		e.t.Fatal(err)
	}
	return f
}

func (e *env) folder(parent *string, name string) db.Node {
	e.t.Helper()
	f, err := e.fs.CreateFolder(context.Background(), e.userID, parent, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// exifConfig defines a writable XMP ContentIdentifier: exiftool cannot write Apple's
// maker-note one into a JPEG, and the reader finds the tag by name in any group.
const exifConfig = `%Image::ExifTool::UserDefined = (
    'Image::ExifTool::XMP::Main' => { dd => { SubDirectory => { TagTable => 'Image::ExifTool::UserDefined::dd' } } },
);
%Image::ExifTool::UserDefined::dd = (
    GROUPS => { 0 => 'XMP', 1 => 'XMP-dd', 2 => 'Image' },
    NAMESPACE => { 'dd' => 'http://ns.example.invalid/dd/1.0/' },
    WRITABLE => 'string',
    ContentIdentifier => { },
);
1;
`

// jpeg writes a small JPEG (w×48) and applies exiftool args.
func (e *env) jpeg(name string, w int, exifArgs ...string) string {
	e.t.Helper()
	p := filepath.Join(e.t.TempDir(), name)
	run(e.t, e.tools.Vips, "black", p, strconv.Itoa(w), "48")
	if len(exifArgs) > 0 {
		cfg := filepath.Join(e.t.TempDir(), "exiftool.cfg")
		if err := os.WriteFile(cfg, []byte(exifConfig), 0o644); err != nil {
			e.t.Fatal(err)
		}
		run(e.t, e.tools.Exiftool, append([]string{"-config", cfg, "-overwrite_original", "-m"}, append(exifArgs, p)...)...)
	}
	return p
}

// mov writes a one-second QuickTime video and applies exiftool args.
func (e *env) mov(name string, exifArgs ...string) string {
	e.t.Helper()
	p := filepath.Join(e.t.TempDir(), name)
	run(e.t, e.tools.Ffmpeg, "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=black:s=64x48:d=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-f", "mov", p)
	if len(exifArgs) > 0 {
		run(e.t, e.tools.Exiftool, append([]string{"-overwrite_original", "-m"}, append(exifArgs, p)...)...)
	}
	return p
}

func (e *env) push(parent *string, name, src string) db.Node {
	e.t.Helper()
	return e.pushMeta(parent, name, src, storage.PushMeta{})
}

func (e *env) pushMeta(parent *string, name, src string, meta storage.PushMeta) db.Node {
	e.t.Helper()
	f, err := os.Open(src)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	r, err := e.fs.PushWithMeta(context.Background(), e.userID, parent, name, nil, "", f, meta)
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Node
}

func (e *env) catchUp() {
	e.t.Helper()
	if err := library.CatchUp(context.Background(), e.q, e.ix, e.uid); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) asset(n db.Node) (db.PhotoAsset, bool) {
	e.t.Helper()
	a, err := e.q.GetPhotoAssetByNode(context.Background(), n.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, false
	} else if err != nil {
		e.t.Fatal(err)
	}
	return a, true
}

func (e *env) mustAsset(n db.Node) db.PhotoAsset {
	e.t.Helper()
	a, ok := e.asset(n)
	if !ok {
		e.t.Fatalf("no asset for %s", n.Name)
	}
	return a
}

func (e *env) count() int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), "SELECT count(*) FROM photo_assets WHERE user_id = $1", e.uid).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// derivs writes fake derivatives of every kind for the asset and returns their paths.
func (e *env) derivs(a db.PhotoAsset) []string {
	e.t.Helper()
	var out []string
	for _, k := range []string{"thumb", "preview", "h264"} {
		p := AssetDerivPath(e.root, a, k)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func gone(t *testing.T, paths []string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("derivative %s still there (%v)", p, err)
		}
	}
}

func TestDerivPath(t *testing.T) {
	id := "0123abcd-0000-0000-0000-000000000000"
	if got, want := DerivPath("/s", "u", id, "thumb"), "/s/.photos/u/01/"+id+"-thumb.jpg"; got != want {
		t.Fatalf("thumb %s, want %s", got, want)
	}
	if got, want := DerivPath("/s", "u", id, "h264"), "/s/.photos/u/01/"+id+"-h264.mp4"; got != want {
		t.Fatalf("h264 %s, want %s", got, want)
	}
}

// An upload into a source is indexed with its metadata; non-media files are not; the
// same version again is a no-op; new content resets the thumbnail; a rename to a
// non-media name removes the asset and its derivatives.
func TestIndexer_IndexAndRemove(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	photo := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64,
		"-DateTimeOriginal=2019:07:14 19:42:00", "-OffsetTimeOriginal=+06:00",
		"-GPSLatitude=42.6475", "-GPSLatitudeRef=N", "-GPSLongitude=77.0820", "-GPSLongitudeRef=E",
		"-Make=Acme", "-Model=Cam 1"))
	if _, err := e.fs.Push(ctx, e.userID, &sid, "notes.txt", nil, "", strings.NewReader("hi")); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if n := e.count(); n != 1 {
		t.Fatalf("%d assets, want 1 (the photo only)", n)
	}
	a := e.mustAsset(photo)
	if a.Kind != "photo" || a.DateSource != "exif" || a.Make.String != "Acme" || a.Model.String != "Cam 1" {
		t.Fatalf("asset %+v", a)
	}
	if !a.TakenAt.Time.Equal(time.Date(2019, 7, 14, 13, 42, 0, 0, time.UTC)) {
		t.Fatalf("taken_at %v", a.TakenAt.Time)
	}
	if !a.TakenLocal.Time.Equal(time.Date(2019, 7, 14, 19, 42, 0, 0, time.UTC)) || a.TzOffsetMin.Int32 != 360 {
		t.Fatalf("taken_local %v tz %v", a.TakenLocal.Time, a.TzOffsetMin)
	}
	if !a.Lat.Valid || !a.Lon.Valid || a.PlaceID.Valid {
		t.Fatalf("lat/lon %v/%v place %v (no geocoder: NULL)", a.Lat, a.Lon, a.PlaceID)
	}
	if a.Width.Int32 != 64 || a.Height.Int32 != 48 || a.ThumbState != "pending" || a.NodeVersion.Int64 != photo.Version {
		t.Fatalf("asset %+v", a)
	}

	// The same version again: nothing is rewritten.
	if err := e.q.SetThumbState(ctx, db.SetThumbStateParams{ID: a.ID, ThumbState: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := e.ix.Index(ctx, e.userID, id(photo), photo.DiskPath.String); err != nil {
		t.Fatal(err)
	}
	if b := e.mustAsset(photo); !b.IndexedAt.Time.Equal(a.IndexedAt.Time) || b.ThumbState != "ready" {
		t.Fatalf("re-index of the same version rewrote the row: %+v", b)
	}

	// New content: re-read, thumbnail pending again.
	photo = e.push(&sid, "a.jpg", e.jpeg("a2.jpg", 80, "-DateTimeOriginal=2021:01:01 10:00:00"))
	e.catchUp()
	b := e.mustAsset(photo)
	if b.ThumbState != "pending" || b.Width.Int32 != 80 || b.NodeVersion.Int64 != photo.Version || b.ID != a.ID {
		t.Fatalf("after new content %+v", b)
	}

	// Renamed to something that is no media: gone with its derivatives.
	files := e.derivs(b)
	if _, err := e.fs.Rename(ctx, e.userID, id(photo), "a.txt"); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if _, ok := e.asset(photo); ok {
		t.Fatal("the renamed file is still in the gallery")
	}
	gone(t, files)
}

// Trash → trashed_at; restore → NULL (thumbnail kept); purge → no row and no
// derivatives. The same for a whole folder.
func TestIndexer_TrashRestorePurge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	photo := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64))
	album := e.folder(&sid, "Album_%")
	aid := id(album)
	inAlbum := e.push(&aid, "b.jpg", e.jpeg("b.jpg", 70))
	e.catchUp()
	a := e.mustAsset(photo)
	if err := e.q.SetThumbState(ctx, db.SetThumbStateParams{ID: a.ID, ThumbState: "ready"}); err != nil {
		t.Fatal(err)
	}

	if err := e.fs.Delete(ctx, e.userID, id(photo)); err != nil {
		t.Fatal(err)
	}
	if err := e.fs.Delete(ctx, e.userID, aid); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if a := e.mustAsset(photo); !a.TrashedAt.Valid {
		t.Fatal("a trashed photo is not in the gallery trash")
	}
	if a := e.mustAsset(inAlbum); !a.TrashedAt.Valid {
		t.Fatal("a photo of a trashed folder is not in the gallery trash")
	}

	if _, err := e.fs.Undelete(ctx, e.userID, id(photo)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.fs.Undelete(ctx, e.userID, aid); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if a := e.mustAsset(photo); a.TrashedAt.Valid || a.ThumbState != "ready" {
		t.Fatalf("restored photo: trashed_at %v thumb %s (want NULL, kept)", a.TrashedAt, a.ThumbState)
	}
	if a := e.mustAsset(inAlbum); a.TrashedAt.Valid {
		t.Fatal("a photo of a restored folder stays in the gallery trash")
	}

	files := e.derivs(e.mustAsset(photo))
	files = append(files, e.derivs(e.mustAsset(inAlbum))...)
	if err := e.fs.Delete(ctx, e.userID, id(photo)); err != nil {
		t.Fatal(err)
	}
	if err := e.fs.Delete(ctx, e.userID, aid); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if err := e.fs.PurgeAll(ctx, e.userID); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if n := e.count(); n != 0 {
		t.Fatalf("%d assets after the purge, want 0", n)
	}
	gone(t, files)
}

// A file or folder moved out of the sources leaves the gallery with its derivatives; a
// folder moved back in comes back.
func TestIndexer_MoveOutOfSource(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	photo := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64))
	album := e.folder(&sid, "Album")
	aid := id(album)
	inAlbum := e.push(&aid, "b.jpg", e.jpeg("b.jpg", 70))
	e.catchUp()
	files := append(e.derivs(e.mustAsset(photo)), e.derivs(e.mustAsset(inAlbum))...)

	if _, err := e.fs.Move(ctx, e.userID, id(photo), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.fs.Move(ctx, e.userID, aid, nil); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if n := e.count(); n != 0 {
		t.Fatalf("%d assets after moving everything out, want 0", n)
	}
	gone(t, files)

	if _, err := e.fs.Move(ctx, e.userID, aid, &sid); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if _, ok := e.asset(inAlbum); !ok {
		t.Fatal("a folder moved back into the source is not indexed")
	}
}

// The same content in two sources is two assets but one card.
func TestIndexer_DuplicateAcrossSources(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.source("A"), e.source("B")
	aid, bid := id(a), id(b)
	file := e.jpeg("same.jpg", 64, "-DateTimeOriginal=2020:03:04 05:06:07")
	e.push(&aid, "same.jpg", file)
	e.push(&bid, "copy.jpg", file)
	e.catchUp()
	if n := e.count(); n != 2 {
		t.Fatalf("%d assets, want 2", n)
	}
	buckets, err := e.q.ListPhotoMonthBuckets(ctx, db.ListPhotoMonthBucketsParams{UserID: e.uid})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].Count != 1 {
		t.Fatalf("buckets %+v, want one month with one card", buckets)
	}
}

// No date in the file: the node's modification time. An XMP sidecar's date wins, and
// its arrival re-reads the photo.
func TestIndexer_DateFallbackModifiedAt(t *testing.T) {
	e := newEnv(t)
	src := e.source("Photos")
	sid := id(src)
	mod := time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)
	photo := e.pushMeta(&sid, "nodate.jpg", e.jpeg("nodate.jpg", 64), storage.PushMeta{ModifiedAt: mod})
	e.catchUp()
	a := e.mustAsset(photo)
	if a.DateSource != "file" || !a.TakenAt.Time.Equal(mod) || a.TzOffsetMin.Valid {
		t.Fatalf("date %v source %s tz %v, want the modification time", a.TakenAt.Time, a.DateSource, a.TzOffsetMin)
	}

	side := filepath.Join(t.TempDir(), "nodate.jpg.xmp")
	run(t, e.tools.Exiftool, "-o", side, "-XMP-exif:DateTimeOriginal=2018-01-02T03:04:05+03:00")
	e.push(&sid, "nodate.jpg.xmp", side)
	e.catchUp()
	a = e.mustAsset(photo)
	if a.DateSource != "xmp" || !a.TakenAt.Time.Equal(time.Date(2018, 1, 2, 0, 4, 5, 0, time.UTC)) {
		t.Fatalf("after the sidecar: date %v source %s", a.TakenAt.Time, a.DateSource)
	}
	if !a.TakenLocal.Time.Equal(time.Date(2018, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("taken_local %v", a.TakenLocal.Time)
	}
}

// A broken file of a source still shows up (dated by the file).
func TestIndexer_BrokenFileStillIndexed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	r, err := e.fs.Push(ctx, e.userID, &sid, "broken.jpg", nil, "", strings.NewReader("not a jpeg"))
	if err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if a := e.mustAsset(r.Node); a.DateSource != "file" || a.Kind != "photo" {
		t.Fatalf("broken file asset %+v", a)
	}
}

// Live Photo by content identifier, through real files: the video becomes the still's
// child and the card counts once.
func TestGroup_LiveByCID(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	other := e.folder(&sid, "Other")
	oid := id(other)
	still := e.push(&sid, "IMG_0001.JPG", e.jpeg("s.jpg", 64,
		"-DateTimeOriginal=2022:06:01 12:00:00", "-XMP-dd:ContentIdentifier=CID-1"))
	// Another folder and another name: only the identifier links them.
	video := e.push(&oid, "clip.MOV", e.mov("v.mov", "-Keys:ContentIdentifier=CID-1"))
	e.catchUp()
	s, v := e.mustAsset(still), e.mustAsset(video)
	if s.LiveCid.String != "CID-1" || v.LiveCid.String != "CID-1" {
		t.Fatalf("live_cid %q / %q", s.LiveCid.String, v.LiveCid.String)
	}
	if v.ParentID != s.ID || v.Role.String != roleLiveVideo || s.ParentID.Valid {
		t.Fatalf("video parent %v role %v; still parent %v", v.ParentID, v.Role, s.ParentID)
	}
	buckets, err := e.q.ListPhotoMonthBuckets(ctx, db.ListPhotoMonthBucketsParams{UserID: e.uid, Live: pgtype.Bool{Bool: true, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].Count != 1 {
		t.Fatalf("live buckets %+v", buckets)
	}

	// The still leaves: the video is a card of its own again.
	if err := e.fs.Delete(ctx, e.userID, id(still)); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if v := e.mustAsset(video); v.ParentID.Valid {
		t.Fatal("the video still hangs on a trashed still")
	}
}

// sourceIn creates a folder under parent and makes it a photo source.
func (e *env) sourceIn(parent *string, name string) db.Node {
	e.t.Helper()
	f := e.folder(parent, name)
	if _, err := e.q.InsertPhotoSource(context.Background(), db.InsertPhotoSourceParams{UserID: e.uid, FolderNodeID: f.ID}); err != nil {
		e.t.Fatal(err)
	}
	return f
}

func present(t *testing.T, paths []string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("derivative %s gone: %v", p, err)
		}
	}
}

// Trashing a folder that CONTAINS a source moves the source's assets to the gallery
// trash (thumbnails kept); restoring it brings them back at once, without a re-read.
func TestIndexer_TrashRestoreParentOfSource(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	parent := e.folder(nil, "P")
	pid := id(parent)
	src := e.sourceIn(&pid, "S")
	sid := id(src)
	photo := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64))
	e.catchUp()
	a := e.mustAsset(photo)
	if err := e.q.SetThumbState(ctx, db.SetThumbStateParams{ID: a.ID, ThumbState: "ready"}); err != nil {
		t.Fatal(err)
	}
	files := e.derivs(a)
	reads := e.ix.reads.Load()

	if err := e.fs.Delete(ctx, e.userID, pid); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	b, ok := e.asset(photo)
	if !ok || !b.TrashedAt.Valid || b.ID != a.ID || b.ThumbState != "ready" {
		t.Fatalf("after trashing the parent: present %v %+v (want trashed, same id, thumb kept)", ok, b)
	}
	present(t, files)

	if _, err := e.fs.Undelete(ctx, e.userID, pid); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	b, ok = e.asset(photo)
	if !ok || b.TrashedAt.Valid || b.ID != a.ID || b.ThumbState != "ready" {
		t.Fatalf("after restoring the parent: present %v %+v (want live, same id, thumb kept)", ok, b)
	}
	present(t, files)
	if n := e.ix.reads.Load(); n != reads {
		t.Fatalf("%d metadata re-reads for a trash and restore, want 0", n-reads)
	}
}

// Renaming or moving a folder that contains a source keeps every asset (same ids,
// thumbnails, derivatives) and reads nothing again.
func TestIndexer_RenameParentOfSource(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	parent := e.folder(nil, "P")
	pid := id(parent)
	other := e.folder(nil, "Q")
	qid := id(other)
	src := e.sourceIn(&pid, "S")
	sid := id(src)
	photo := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64))
	e.catchUp()
	a := e.mustAsset(photo)
	if err := e.q.SetThumbState(ctx, db.SetThumbStateParams{ID: a.ID, ThumbState: "ready"}); err != nil {
		t.Fatal(err)
	}
	files := e.derivs(a)
	reads := e.ix.reads.Load()

	if _, err := e.fs.Rename(ctx, e.userID, pid, "P2"); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	if _, err := e.fs.Move(ctx, e.userID, pid, &qid); err != nil {
		t.Fatal(err)
	}
	e.catchUp()
	b, ok := e.asset(photo)
	if !ok || b.ID != a.ID || b.ThumbState != "ready" || b.TrashedAt.Valid {
		t.Fatalf("after rename and move of the parent: present %v %+v", ok, b)
	}
	present(t, files)
	if n := e.ix.reads.Load(); n != reads {
		t.Fatalf("%d metadata re-reads for a rename and a move, want 0", n-reads)
	}
}

// A Reindex (API) and the tracker's catch-up running at once must leave the groups
// right: regroups of one user are serialised. Run with -race.
func TestIndexer_ReindexConcurrentWithCatchUp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	orig := e.push(&sid, "IMG_0001.JPG", e.jpeg("o.jpg", 64, "-DateTimeOriginal=2022:06:01 12:00:00"))
	edit := e.push(&sid, "IMG_E0001.JPG", e.jpeg("e.jpg", 66, "-DateTimeOriginal=2022:06:01 12:00:00"))
	still := e.push(&sid, "IMG_0002.JPG", e.jpeg("s.jpg", 68, "-DateTimeOriginal=2022:06:01 13:00:00", "-OffsetTimeOriginal=+06:00"))
	// No identifiers: the pair comes from the name-and-time fallback.
	video := e.push(&sid, "IMG_0002.MOV", e.mov("v.mov", "-Keys:CreationDate=2022:06:01 13:00:01+06:00"))
	for round := 0; round < 3; round++ {
		done := make(chan error, 2)
		go func() { done <- e.ix.Reindex(ctx, e.uid) }()
		go func() { done <- library.CatchUp(ctx, e.q, e.ix, e.uid) }()
		for range 2 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		o, ed, s, v := e.mustAsset(orig), e.mustAsset(edit), e.mustAsset(still), e.mustAsset(video)
		if o.ParentID != ed.ID || o.Role.String != roleOriginal || ed.ParentID.Valid {
			t.Fatalf("round %d: edit pair %v/%v", round, o.ParentID, o.Role)
		}
		if v.ParentID != s.ID || v.Role.String != roleLiveVideo || s.ParentID.Valid {
			t.Fatalf("round %d: live pair %v/%v", round, v.ParentID, v.Role)
		}
		// Something new for the next round's catch-up.
		e.push(&sid, "x"+strconv.Itoa(round)+".jpg", e.jpeg("x.jpg", 70+round))
	}
}

// A full pass over a big archive reads in batches and lets the catch-up of new uploads in
// between: an upload shows up (and is regrouped) while the pass still runs, Heal returns
// at once (the tracker calls it from its LISTEN loop), and the progress counts what the
// pass has yet to read.
func TestIndexer_FullPassInterleavesWithCatchUp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	for i := 0; i < 6; i++ {
		e.push(&sid, "old"+strconv.Itoa(i)+".jpg", e.jpeg("o.jpg", 64+i))
	}
	seq, err := e.q.GetUserChangeSeq(ctx, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.ix.SetCursor(ctx, e.uid, seq); err != nil { // the archive is only the pass's
		t.Fatal(err)
	}

	e.ix.batch = 2
	var upload db.Node
	caughtUp := make(chan error, 1)
	batches := 0
	var progress []int64
	release := make(chan struct{})
	e.ix.afterBatch = func() {
		batches++
		p, scanning := e.ix.Progress(e.uid)
		if !scanning {
			t.Error("Progress does not report the running pass")
		}
		progress = append(progress, p)
		switch batches {
		case 1:
			<-release // Heal has returned meanwhile
			upload = e.push(&sid, "new.jpg", e.jpeg("n.jpg", 90))
			go func() { caughtUp <- library.CatchUp(ctx, e.q, e.ix, e.uid) }()
		case 2:
			// The catch-up (its regroup takes the user's lock) finishes while the pass is
			// between batches; with the lock held for the whole pass this would never come.
			select {
			case err := <-caughtUp:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(30 * time.Second):
				t.Error("the catch-up waited for the whole pass")
			}
			if _, ok := e.asset(upload); !ok {
				t.Error("the upload is not in the gallery while the pass runs")
			}
		}
	}
	if err := e.ix.Heal(ctx, e.uid); err != nil {
		t.Fatal(err)
	}
	close(release) // Heal returned before its first batch ended
	e.ix.Wait()
	if batches != 3 {
		t.Fatalf("%d batches, want 3", batches)
	}
	if len(progress) != 3 || progress[0] != 4 || progress[1] != 2 || progress[2] != 0 {
		t.Fatalf("pending after each batch %v, want [4 2 0]", progress)
	}
	if p, scanning := e.ix.Progress(e.uid); p != 0 || scanning {
		t.Fatalf("after the pass: pending %d scanning %v", p, scanning)
	}
	if n := e.count(); n != 7 {
		t.Fatalf("%d assets, want 7", n)
	}
}

// A file whose metadata cannot be read is still shown and is recorded for the
// failed-files list; a good re-read clears the record.
func TestIndexer_MetadataFailureRecorded(t *testing.T) {
	e := newEnv(t)
	src := e.source("Photos")
	sid := id(src)
	n := e.push(&sid, "bad.dng", filepathWrite(t, "bad.dng", []byte("II*\x00garbage")))
	e.catchUp()
	a := e.mustAsset(n)
	if !a.IndexError.Valid || a.IndexError.String == "" {
		t.Fatalf("index_error not recorded: %+v", a.IndexError)
	}
	// Rewritten as a readable file (a TIFF is what a DNG is made of).
	good := filepath.Join(t.TempDir(), "good.tif")
	run(t, e.tools.Vips, "black", good, "8", "8")
	e.overwrite(n, good)
	e.catchUp()
	if b := e.mustAsset(n); b.IndexError.Valid {
		t.Fatalf("index_error kept after a good read: %q", b.IndexError.String)
	}
}
