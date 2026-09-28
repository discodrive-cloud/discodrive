package library_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/ebook"
	"discodrive/internal/library"
	"discodrive/internal/music"
	"discodrive/internal/music/musictest"
	"discodrive/internal/storage"
)

type env struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	fs     *storage.FileService
	root   string
	userID string
	uid    pgtype.UUID
}

func newEnv(t *testing.T) *env {
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
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "l@x", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return &env{pool: pool, q: q, fs: storage.NewFileService(pool, storage.NewLocalDisk(root)),
		root: root, userID: db.UUIDString(u.ID), uid: u.ID}
}

// fakeLib records what the change log asked of it.
type fakeLib struct {
	mu       sync.Mutex
	folder   string
	cursor   int64
	indexed  map[string]bool
	removed  map[string]bool
	underIdx []string
	underRm  []string
}

func newFake(folder string) *fakeLib {
	return &fakeLib{folder: folder, indexed: map[string]bool{}, removed: map[string]bool{}}
}
func (f *fakeLib) Name() string { return "fake" }
func (f *fakeLib) State(context.Context, pgtype.UUID) (string, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.folder, f.cursor, true, nil
}
func (f *fakeLib) SetCursor(_ context.Context, _ pgtype.UUID, s int64) error {
	f.mu.Lock()
	f.cursor = s
	f.mu.Unlock()
	return nil
}
func (f *fakeLib) Accepts(p string) bool { return strings.HasSuffix(p, ".mp3") }
func (f *fakeLib) Index(_ context.Context, _, id, _ string) error {
	f.mu.Lock()
	f.indexed[id] = true
	f.mu.Unlock()
	return nil
}
func (f *fakeLib) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	f.removed[id] = true
	f.mu.Unlock()
	return nil
}
func (f *fakeLib) IndexUnder(_ context.Context, _, dirID string) error {
	f.mu.Lock()
	f.underIdx = append(f.underIdx, dirID)
	f.mu.Unlock()
	return nil
}
func (f *fakeLib) RemoveUnder(_ context.Context, _ pgtype.UUID, dirPath string) error {
	f.mu.Lock()
	f.underRm = append(f.underRm, dirPath)
	f.mu.Unlock()
	return nil
}

func (f *fakeLib) Heal(context.Context, pgtype.UUID) error { return nil }

func id(n db.Node) string { return db.UUIDString(n.ID) }

func push(t *testing.T, e *env, parent *string, name string) db.Node {
	t.Helper()
	r, err := e.fs.Push(context.Background(), e.userID, parent, name, nil, "", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	return r.Node
}

// Uploads into the folder are indexed, trashed or moved-out files removed, the rest
// ignored; the cursor ends at the user's last change.
func TestCatchUpFollowsTheChangeLog(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	music, _ := e.fs.CreateFolder(ctx, e.userID, nil, "Music")
	mid := id(music)
	lib := newFake(music.DiskPath.String)

	in := push(t, e, &mid, "a.mp3")
	trashed := push(t, e, &mid, "b.mp3")
	moved := push(t, e, &mid, "c.mp3")
	outside := push(t, e, nil, "d.mp3")
	notAudio := push(t, e, &mid, "e.txt")
	if err := e.fs.Delete(ctx, e.userID, id(trashed)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.fs.Move(ctx, e.userID, id(moved), nil); err != nil {
		t.Fatal(err)
	}

	if err := library.CatchUp(ctx, e.q, lib, e.uid); err != nil {
		t.Fatal(err)
	}
	if !lib.indexed[id(in)] || lib.indexed[id(outside)] || lib.indexed[id(notAudio)] {
		t.Fatalf("indexed %v", lib.indexed)
	}
	if !lib.removed[id(trashed)] || !lib.removed[id(moved)] {
		t.Fatalf("removed %v: want the trashed and the moved-out file", lib.removed)
	}
	if seq, _ := e.q.GetUserChangeSeq(ctx, e.uid); lib.cursor != seq {
		t.Fatalf("cursor %d, want %d", lib.cursor, seq)
	}
}

// Changes made while nothing was listening are picked up from the cursor, and only they.
func TestCatchUpResumesFromCursor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	music, _ := e.fs.CreateFolder(ctx, e.userID, nil, "Music")
	mid := id(music)
	old := push(t, e, &mid, "old.mp3")
	first := newFake(music.DiskPath.String)
	if err := library.CatchUp(ctx, e.q, first, e.uid); err != nil {
		t.Fatal(err)
	}
	fresh := push(t, e, &mid, "new.mp3")
	second := newFake(music.DiskPath.String)
	second.cursor = first.cursor // as persisted
	if err := library.CatchUp(ctx, e.q, second, e.uid); err != nil {
		t.Fatal(err)
	}
	if second.indexed[id(old)] || !second.indexed[id(fresh)] {
		t.Fatalf("indexed %v: want only the new file", second.indexed)
	}
}

// Trashing a folder logs only the folder; restoring it, too. The library must drop and
// then re-index everything under it.
func TestCatchUpHandlesWholeFolders(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	music, _ := e.fs.CreateFolder(ctx, e.userID, nil, "Music")
	mid := id(music)
	album, _ := e.fs.CreateFolder(ctx, e.userID, &mid, "Album")
	aid := id(album)
	push(t, e, &aid, "t1.mp3")
	lib := newFake(music.DiskPath.String)
	if err := library.CatchUp(ctx, e.q, lib, e.uid); err != nil {
		t.Fatal(err)
	}

	if err := e.fs.Delete(ctx, e.userID, aid); err != nil {
		t.Fatal(err)
	}
	if err := library.CatchUp(ctx, e.q, lib, e.uid); err != nil {
		t.Fatal(err)
	}
	if len(lib.underRm) != 1 || lib.underRm[0] != album.DiskPath.String {
		t.Fatalf("remove-under %v: want the trashed album folder", lib.underRm)
	}

	if _, err := e.fs.Undelete(ctx, e.userID, aid); err != nil {
		t.Fatal(err)
	}
	if err := library.CatchUp(ctx, e.q, lib, e.uid); err != nil {
		t.Fatal(err)
	}
	if len(lib.underIdx) == 0 || lib.underIdx[len(lib.underIdx)-1] != aid {
		t.Fatalf("index-under %v: want the restored album folder", lib.underIdx)
	}
}

// Trashing the library folder itself drops everything in it.
func TestCatchUpDropsTrashedLibraryFolder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	music, _ := e.fs.CreateFolder(ctx, e.userID, nil, "Music")
	mid := id(music)
	push(t, e, &mid, "a.mp3")
	lib := newFake(music.DiskPath.String)
	if err := library.CatchUp(ctx, e.q, lib, e.uid); err != nil {
		t.Fatal(err)
	}
	if err := e.fs.Delete(ctx, e.userID, mid); err != nil {
		t.Fatal(err)
	}
	if err := library.CatchUp(ctx, e.q, lib, e.uid); err != nil {
		t.Fatal(err)
	}
	if len(lib.underRm) != 1 || lib.underRm[0] != music.DiskPath.String {
		t.Fatalf("remove-under %v: want the library folder itself", lib.underRm)
	}
}

// Renaming the library folder moves every path; nothing may be removed.
func TestCatchUpSurvivesFolderRename(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	music, _ := e.fs.CreateFolder(ctx, e.userID, nil, "Music")
	mid := id(music)
	push(t, e, &mid, "a.mp3")
	lib := newFake(music.DiskPath.String)
	if err := library.CatchUp(ctx, e.q, lib, e.uid); err != nil {
		t.Fatal(err)
	}
	renamed, err := e.fs.Rename(ctx, e.userID, mid, "Tunes")
	if err != nil {
		t.Fatal(err)
	}
	lib.folder = renamed.DiskPath.String // State() reads the folder node's current path
	if err := library.CatchUp(ctx, e.q, lib, e.uid); err != nil {
		t.Fatal(err)
	}
	if len(lib.removed) != 0 || len(lib.underRm) != 0 {
		t.Fatalf("a rename removed %v / %v", lib.removed, lib.underRm)
	}
}

// The tracker catches a user up shortly after a change is logged.
func TestTrackerRunsOnNotify(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	music, _ := e.fs.CreateFolder(ctx, e.userID, nil, "Music")
	mid := id(music)
	lib := newFake(music.DiskPath.String)
	go library.NewTracker(e.pool, lib).Run(ctx)
	time.Sleep(300 * time.Millisecond) // let it LISTEN and do its startup catch-up
	n := push(t, e, &mid, "a.mp3")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lib.mu.Lock()
		ok := lib.indexed[id(n)]
		lib.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("upload not indexed within 5 s")
}

// A trashed track leaves the real music library (bug: it stayed until the trash purge).
func TestTrashedTrackLeavesMusicLibrary(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	musictest.RequireFFmpeg(t)
	folder, _ := e.fs.CreateFolder(ctx, e.userID, nil, "Music")
	fid := id(folder)
	track := pushAudio(t, e, &fid, "a.mp3") // synthesizes an mp3 and pushes it
	ix := music.NewIndexer(e.q, e.root)
	if _, err := e.q.UpsertMusicSettings(ctx, db.UpsertMusicSettingsParams{UserID: e.uid, Enabled: true,
		FolderNodeID: folder.ID, TagEditVersioning: true}); err != nil {
		t.Fatal(err)
	}
	if err := library.CatchUp(ctx, e.q, ix, e.uid); err != nil {
		t.Fatal(err)
	}
	if songs, _ := e.q.AccessibleSongs(ctx, e.uid); len(songs) != 1 {
		t.Fatalf("%d songs after upload, want 1", len(songs))
	}
	if err := e.fs.Delete(ctx, e.userID, id(track)); err != nil {
		t.Fatal(err)
	}
	if err := library.CatchUp(ctx, e.q, ix, e.uid); err != nil {
		t.Fatal(err)
	}
	if songs, _ := e.q.AccessibleSongs(ctx, e.uid); len(songs) != 0 {
		t.Fatalf("a trashed track is still in the library (%d songs)", len(songs))
	}
}

// pushAudio synthesizes a short mp3 and uploads it through the file service.
func pushAudio(t *testing.T, e *env, parent *string, name string) db.Node {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	musictest.SynthesizeAudio(t, src, name, "Artist", "Album", "mp3")
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := e.fs.Push(context.Background(), e.userID, parent, name, nil, "", f)
	if err != nil {
		t.Fatal(err)
	}
	return r.Node
}

const tinyFB2 = `<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0"><description><title-info>
<author><first-name>Ann</first-name><last-name>Writer</last-name></author>
<book-title>Tiny Book</book-title><lang>en</lang></title-info></description>
<body><section><p>Hello.</p></section></body></FictionBook>`

// The same for the e-book library: an upload is indexed, the trash takes it out.
func TestTrashedBookLeavesEbookLibrary(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	folder, _ := e.fs.CreateFolder(ctx, e.userID, nil, "Books")
	fid := id(folder)
	r, err := e.fs.Push(ctx, e.userID, &fid, "tiny.fb2", nil, "", strings.NewReader(tinyFB2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.UpsertEbookSettings(ctx, db.UpsertEbookSettingsParams{UserID: e.uid, Enabled: true, FolderNodeID: folder.ID}); err != nil {
		t.Fatal(err)
	}
	ix := ebook.NewIndexer(e.q, e.root)
	books := func() int {
		var n int
		if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM books WHERE user_id = $1", e.uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := library.CatchUp(ctx, e.q, ix, e.uid); err != nil {
		t.Fatal(err)
	}
	if n := books(); n != 1 {
		t.Fatalf("%d books after upload, want 1", n)
	}
	if err := e.fs.Delete(ctx, e.userID, id(r.Node)); err != nil {
		t.Fatal(err)
	}
	if err := library.CatchUp(ctx, e.q, ix, e.uid); err != nil {
		t.Fatal(err)
	}
	if n := books(); n != 0 {
		t.Fatalf("a trashed book is still in the library (%d)", n)
	}
}

// A row the change log already went past — a failed index, or a file missed when the
// cursor was started at the present — must not stay out of the library for good, and
// rows outside the library folder must go: Heal does both.
func TestHealRepairsWhatTheCursorSkipped(t *testing.T) {
	musictest.RequireFFmpeg(t)
	e := newEnv(t)
	ctx := context.Background()
	folder, _ := e.fs.CreateFolder(ctx, e.userID, nil, "Music")
	fid := id(folder)
	skipped := pushAudio(t, e, &fid, "skipped.mp3")
	outside := pushAudio(t, e, nil, "outside.mp3")
	ix := music.NewIndexer(e.q, e.root)
	if _, err := e.q.UpsertMusicSettings(ctx, db.UpsertMusicSettingsParams{UserID: e.uid, Enabled: true,
		FolderNodeID: folder.ID, TagEditVersioning: true}); err != nil {
		t.Fatal(err)
	}
	seq, _ := e.q.GetUserChangeSeq(ctx, e.uid)
	if err := ix.SetCursor(ctx, e.uid, seq); err != nil { // the log is "done"
		t.Fatal(err)
	}
	// A stale row outside the folder (e.g. left by an earlier folder).
	if err := ix.Index(ctx, e.userID, id(outside), outside.DiskPath.String); err != nil {
		t.Fatal(err)
	}

	if err := ix.Heal(ctx, e.uid); err != nil {
		t.Fatal(err)
	}
	songs, _ := e.q.AccessibleSongs(ctx, e.uid)
	if len(songs) != 1 || songs[0].NodeID != skipped.ID {
		t.Fatalf("songs after heal %+v: want only the skipped track", songs)
	}
}

// The cursor never moves backwards: a catch-up that read the settings before a folder
// switch must not undo the switch's cursor.
func TestCursorNeverMovesBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.q.UpsertMusicSettings(ctx, db.UpsertMusicSettingsParams{UserID: e.uid, Enabled: true, TagEditVersioning: true}); err != nil {
		t.Fatal(err)
	}
	ix := music.NewIndexer(e.q, e.root)
	if err := ix.SetCursor(ctx, e.uid, 10); err != nil {
		t.Fatal(err)
	}
	if err := ix.SetCursor(ctx, e.uid, 4); err != nil {
		t.Fatal(err)
	}
	ms, _ := e.q.GetMusicSettings(ctx, e.uid)
	if ms.IndexedSeq != 10 {
		t.Fatalf("cursor %d after setting 10 then 4, want 10", ms.IndexedSeq)
	}
}
