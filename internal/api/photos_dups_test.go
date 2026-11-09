package api

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

type dupsResp struct {
	Groups []struct {
		Assets []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Favorite bool   `json:"favorite"`
			Thumb    string `json:"thumb"`
			Name     string `json:"name"`
			Path     string `json:"path"`
			Format   string `json:"format"`
			Bytes    int64  `json:"bytes"`
			Copies   int    `json:"copies"`
		} `json:"assets"`
	} `json:"groups"`
	ReclaimBytes int64 `json:"reclaimBytes"`
	Progress     struct {
		Hashed   int64 `json:"hashed"`
		Total    int64 `json:"total"`
		Scanning bool  `json:"scanning"`
	} `json:"progress"`
}

func hp(v uint64) *uint64 { return &v }

const dupHash = 0x0f0f_3c3c_a5a5_9696

func (e *photosEnv) dups() dupsResp {
	e.t.Helper()
	var out dupsResp
	e.json(e.do(e.tok, "GET", "/me/photos/v1/duplicates", nil), 200, &out)
	return out
}

// dupPair makes one frame twice: a JPEG and its RAW, the RAW bigger.
func (e *photosEnv) dupPair(stem string, taken time.Time) (jpg, dng db.PhotoAsset) {
	e.t.Helper()
	jpg = e.asset(e.user, assetOpts{name: stem + ".JPG", phash: hp(dupHash), make: "Apple", size: 1000, taken: taken})
	dng = e.asset(e.user, assetOpts{name: stem + ".DNG", phash: hp(dupHash ^ 1), make: "Apple", size: 5000, taken: taken})
	return jpg, dng
}

func (e *photosEnv) resolve(tok string, keep db.PhotoAsset, del ...db.PhotoAsset) (int, map[string]any) {
	e.t.Helper()
	ids := make([]string, len(del))
	for i, d := range del {
		ids[i] = db.UUIDString(d.ID)
	}
	rec := e.do(tok, "POST", "/me/photos/v1/duplicates/resolve", map[string]any{"keep": db.UUIDString(keep.ID), "delete": ids})
	var out map[string]any
	if rec.Code == 200 {
		e.json(rec, 200, &out)
	}
	return rec.Code, out
}

func (e *photosEnv) assertLive(assets ...db.PhotoAsset) {
	e.t.Helper()
	for _, a := range assets {
		got, err := e.q.GetPhotoAsset(e.ctx, db.GetPhotoAssetParams{ID: a.ID, UserID: a.UserID})
		if err != nil || got.TrashedAt.Valid {
			e.t.Fatalf("asset %s not live: %v %v", db.UUIDString(a.ID), got.TrashedAt, err)
		}
		if n, _ := e.q.GetNodeAnyState(e.ctx, a.NodeID); n.DeletedAt.Valid {
			e.t.Fatalf("file of %s trashed", db.UUIDString(a.ID))
		}
	}
}

func (e *photosEnv) assertTrashed(assets ...db.PhotoAsset) {
	e.t.Helper()
	for _, a := range assets {
		got, _ := e.q.GetPhotoAsset(e.ctx, db.GetPhotoAssetParams{ID: a.ID, UserID: a.UserID})
		if !got.TrashedAt.Valid {
			e.t.Fatalf("asset %s not in the gallery trash", db.UUIDString(a.ID))
		}
		n, err := e.q.GetNodeAnyState(e.ctx, a.NodeID)
		if err != nil || !n.DeletedAt.Valid {
			e.t.Fatalf("file of %s not in the file trash: %v", db.UUIDString(a.ID), err)
		}
	}
}

func TestDupsList_Basic(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	jpg, dng := e.dupPair("IMG_1170", time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	unhashed := e.asset(e.user, assetOpts{}) // thumbnail ready, not hashed yet: progress only
	if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET thumb_state = 'ready' WHERE id = $1`, unhashed.ID); err != nil {
		t.Fatal(err)
	}
	e.asset(e.user, assetOpts{}) // no thumbnail yet: not counted at all
	flat := e.asset(e.user, assetOpts{name: "black.JPG", phash: hp(dupHash), make: "Apple", taken: time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)})
	if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET phash_flat = true WHERE id = $1`, flat.ID); err != nil {
		t.Fatal(err)
	}
	out := e.dups()
	if len(out.Groups) != 1 || len(out.Groups[0].Assets) != 2 {
		t.Fatalf("groups = %+v", out.Groups)
	}
	g := out.Groups[0].Assets
	if g[0].ID != db.UUIDString(jpg.ID) || g[1].ID != db.UUIDString(dng.ID) {
		t.Fatalf("order = %s, %s; want JPEG first", g[0].Name, g[1].Name)
	}
	if g[1].Name != "IMG_1170.DNG" || g[1].Format != "DNG" || g[1].Bytes != 5000 || g[1].Copies != 1 || g[1].Path != "" || g[1].Kind != "photo" || g[1].Thumb == "" {
		t.Fatalf("dng = %+v", g[1])
	}
	if g[0].Format != "JPG" || g[0].Bytes != 1000 {
		t.Fatalf("jpg = %+v", g[0])
	}
	if out.ReclaimBytes != 5000 {
		t.Fatalf("reclaimBytes = %d, want 5000", out.ReclaimBytes)
	}
	if out.Progress.Hashed != 3 || out.Progress.Total != 4 || out.Progress.Scanning {
		t.Fatalf("progress = %+v", out.Progress)
	}
	// The other user sees nothing of it.
	var other dupsResp
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/duplicates", nil), 200, &other)
	if len(other.Groups) != 0 || other.ReclaimBytes != 0 {
		t.Fatalf("user2 = %+v", other)
	}
}

func TestDupsResolve_MovesToTrash(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	jpg, dng := e.dupPair("IMG_1", time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	code, res := e.resolve(e.tok, jpg, dng)
	if code != 200 || res["deletedNodes"] != float64(1) {
		t.Fatalf("resolve = %d %v", code, res)
	}
	e.assertTrashed(dng)
	e.assertLive(jpg)
	rec := e.do(e.tok, "GET", "/me/photos/v1/trash", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), db.UUIDString(dng.ID)) {
		t.Fatalf("gallery trash = %d %s", rec.Code, rec.Body.String())
	}
	if out := e.dups(); len(out.Groups) != 0 || out.ReclaimBytes != 0 {
		t.Fatalf("group still listed: %+v", out)
	}
}

func TestDupsResolve_CopiesFavoriteAndAlbums(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	taken := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	jpg, dng := e.dupPair("IMG_2", taken)
	// Byte copies: of the kept JPEG (follows its favorite), of the DNG (no hash: the DNG
	// stays the group's card; it goes with the DNG and its album counts too).
	jpgCopy := e.asset(e.user, assetOpts{name: "IMG_2 copy.JPG", taken: taken, size: 1000})
	dngCopy := e.asset(e.user, assetOpts{name: "IMG_2 (1).DNG", taken: taken, size: 5000})
	for _, p := range [][2]db.PhotoAsset{{jpg, jpgCopy}, {dng, dngCopy}} {
		if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET content_hash = $2 WHERE id = ANY($1::uuid[])`,
			[]pgtype.UUID{p[0].ID, p[1].ID}, db.UUIDString(p[0].ID)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET favorite = true WHERE id = $1`, dng.ID); err != nil {
		t.Fatal(err)
	}
	trip, err := e.q.CreatePhotoAlbum(e.ctx, db.CreatePhotoAlbumParams{OwnerID: e.user.ID, Name: "Trip"})
	if err != nil {
		t.Fatal(err)
	}
	home, err := e.q.CreatePhotoAlbum(e.ctx, db.CreatePhotoAlbumParams{OwnerID: e.user.ID, Name: "Home"})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []struct {
		al pgtype.UUID
		a  db.PhotoAsset
	}{{trip.ID, dng}, {home.ID, dngCopy}, {home.ID, jpg}} {
		if _, err := e.q.AddPhotoAlbumItems(e.ctx, db.AddPhotoAlbumItemsParams{AlbumID: it.al, AssetIds: []pgtype.UUID{it.a.ID}, AddedBy: e.user.ID}); err != nil {
			t.Fatal(err)
		}
	}
	out := e.dups()
	if len(out.Groups) != 1 || out.Groups[0].Assets[1].Copies != 2 || out.Groups[0].Assets[1].Bytes != 10000 {
		t.Fatalf("groups = %+v", out.Groups)
	}

	code, res := e.resolve(e.tok, jpg, dng)
	if code != 200 || res["deletedNodes"] != float64(2) {
		t.Fatalf("resolve = %d %v", code, res)
	}
	e.assertTrashed(dng, dngCopy)
	e.assertLive(jpg, jpgCopy)
	for _, a := range []db.PhotoAsset{jpg, jpgCopy} {
		got, _ := e.q.GetPhotoAssetByID(e.ctx, a.ID)
		if !got.Favorite {
			t.Fatalf("%s not favorite after resolve", db.UUIDString(a.ID))
		}
	}
	items := func(al pgtype.UUID) map[pgtype.UUID]bool {
		rows, err := e.pool.Query(e.ctx, `SELECT asset_id FROM photo_album_items WHERE album_id = $1`, al)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		m := map[pgtype.UUID]bool{}
		for rows.Next() {
			var id pgtype.UUID
			_ = rows.Scan(&id)
			m[id] = true
		}
		return m
	}
	if got := items(trip.ID); len(got) != 2 || !got[jpg.ID] || !got[dng.ID] {
		t.Fatalf("Trip = %v; want the JPEG added and the DNG row kept", got)
	}
	if got := items(home.ID); len(got) != 2 || !got[jpg.ID] || !got[dngCopy.ID] {
		t.Fatalf("Home = %v", got)
	}
	if got, _ := e.q.GetPhotoAssetByID(e.ctx, dng.ID); !got.Favorite {
		t.Fatal("the trashed DNG lost its favorite")
	}
}

func TestDupsResolve_KeepSurvives(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	jpg, dng := e.dupPair("IMG_3", time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	if code, _ := e.resolve(e.tok, jpg, jpg, dng); code != 400 {
		t.Fatalf("keep in delete = %d, want 400", code)
	}
	if code, _ := e.resolve(e.tok, jpg); code != 400 {
		t.Fatalf("empty delete = %d, want 400", code)
	}
	if code, _ := e.resolve(e.tok, jpg, dng, dng); code != 400 {
		t.Fatalf("repeated id = %d, want 400", code)
	}
	if c := e.do(e.tok, "POST", "/me/photos/v1/duplicates/resolve", map[string]any{"keep": "nope", "delete": []string{db.UUIDString(dng.ID)}}).Code; c != 400 {
		t.Fatalf("bad keep = %d, want 400", c)
	}
	e.assertLive(jpg, dng)
	if out := e.dups(); len(out.Groups) != 1 {
		t.Fatalf("groups = %+v", out.Groups)
	}
}

func TestDupsResolve_RejectsNotOneGroup(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	a1, a2 := e.dupPair("IMG_A", time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	b1, b2 := e.dupPair("IMG_B", time.Date(2026, 2, 5, 10, 0, 0, 0, time.UTC))
	lone := e.asset(e.user, assetOpts{name: "lone.jpg", phash: hp(^uint64(dupHash)), taken: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)})
	if code, _ := e.resolve(e.tok, a1, b2); code != 400 {
		t.Fatalf("across groups = %d, want 400", code)
	}
	if code, _ := e.resolve(e.tok, a1, a2, b2); code != 400 {
		t.Fatalf("one extra from another group = %d, want 400", code)
	}
	if code, _ := e.resolve(e.tok, a1, lone); code != 400 {
		t.Fatalf("not a duplicate = %d, want 400", code)
	}
	e.assertLive(a1, a2, b1, b2, lone)
}

func TestDupsResolve_RejectsForeign(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	jpg, dng := e.dupPair("IMG_4", time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	theirs := e.asset(e.user2, assetOpts{name: "IMG_4.DNG", phash: hp(dupHash), make: "Apple", taken: time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)})
	if code, _ := e.resolve(e.tok, jpg, dng, theirs); code != 404 {
		t.Fatalf("foreign delete = %d, want 404", code)
	}
	if code, _ := e.resolve(e.tok, theirs, dng); code != 404 {
		t.Fatalf("foreign keep = %d, want 404", code)
	}
	// The owner of the group cannot be acted on by someone else either.
	if code, _ := e.resolve(e.tok2, jpg, dng); code != 404 {
		t.Fatalf("other user = %d, want 404", code)
	}
	e.assertLive(jpg, dng, theirs)
}

func TestDupsResolve_RejectsHiddenTrashed(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	taken := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	jpg, dng := e.dupPair("IMG_5", taken)
	hidden := e.asset(e.user, assetOpts{name: "IMG_5 copy.JPG", phash: hp(dupHash), make: "Apple", taken: taken, hidden: true})
	trashed := e.asset(e.user, assetOpts{name: "IMG_5 (2).JPG", phash: hp(dupHash), make: "Apple", taken: taken, trash: true})
	if code, _ := e.resolve(e.tok, jpg, dng, hidden); code != 404 {
		t.Fatalf("hidden = %d, want 404", code)
	}
	if code, _ := e.resolve(e.tok, jpg, dng, trashed); code != 404 {
		t.Fatalf("trashed = %d, want 404", code)
	}
	if code, _ := e.resolve(e.tok, trashed, dng); code != 404 {
		t.Fatalf("trashed keep = %d, want 404", code)
	}
	e.assertLive(jpg, dng)
	if out := e.dups(); len(out.Groups) != 1 || len(out.Groups[0].Assets) != 2 {
		t.Fatalf("groups = %+v", out.Groups)
	}
}

func dismiss(e *photosEnv, tok string, as ...db.PhotoAsset) int {
	ids := make([]string, len(as))
	for i, a := range as {
		ids[i] = db.UUIDString(a.ID)
	}
	return e.do(tok, "POST", "/me/photos/v1/duplicates/dismiss", map[string]any{"ids": ids}).Code
}

func TestDupsDismiss_Hides(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	jpg, dng := e.dupPair("IMG_6", time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	b1, b2 := e.dupPair("IMG_7", time.Date(2026, 2, 5, 10, 0, 0, 0, time.UTC))
	if c := dismiss(e, e.tok, jpg); c != 400 {
		t.Fatalf("one id = %d, want 400", c)
	}
	if c := dismiss(e, e.tok, jpg, b2); c != 400 {
		t.Fatalf("across groups = %d, want 400", c)
	}
	if c := dismiss(e, e.tok2, jpg, dng); c != 404 {
		t.Fatalf("other user = %d, want 404", c)
	}
	if out := e.dups(); len(out.Groups) != 2 {
		t.Fatalf("refused dismiss changed groups: %+v", out.Groups)
	}
	if c := dismiss(e, e.tok, dng, jpg); c != 204 {
		t.Fatalf("dismiss = %d, want 204", c)
	}
	for i := 0; i < 2; i++ {
		out := e.dups()
		if len(out.Groups) != 1 || out.Groups[0].Assets[0].ID != db.UUIDString(b1.ID) {
			t.Fatalf("GET %d: groups = %+v", i, out.Groups)
		}
		if out.ReclaimBytes != 5000 {
			t.Fatalf("reclaimBytes = %d", out.ReclaimBytes)
		}
	}
	e.assertLive(jpg, dng, b1, b2)
}

func TestDupsDismiss_NewCopyReturns(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	taken := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	jpg, dng := e.dupPair("IMG_8", taken)
	if c := dismiss(e, e.tok, jpg, dng); c != 204 {
		t.Fatalf("dismiss = %d", c)
	}
	if out := e.dups(); len(out.Groups) != 0 {
		t.Fatalf("groups = %+v", out.Groups)
	}
	third := e.asset(e.user, assetOpts{name: "IMG_8.HEIC", phash: hp(dupHash ^ 2), make: "Apple", size: 800, taken: taken})
	// The new copy brings a group back. The dismissed pair stays apart (complete linkage):
	// the new copy pairs with one of them.
	out := e.dups()
	if len(out.Groups) != 1 || len(out.Groups[0].Assets) != 2 {
		t.Fatalf("groups = %+v", out.Groups)
	}
	got := map[string]bool{}
	for _, a := range out.Groups[0].Assets {
		got[a.ID] = true
	}
	if !got[db.UUIDString(third.ID)] || got[db.UUIDString(jpg.ID)] == got[db.UUIDString(dng.ID)] {
		t.Fatalf("want the new copy with one of the dismissed pair: %+v", out.Groups[0].Assets)
	}
}

func TestDupsFolder(t *testing.T) {
	u := "0b6f2a1e-0000-4000-8000-000000000001"
	for in, want := range map[string]string{
		u + "/IMG.DNG":                "",
		u + "/photos/2026/01/IMG.DNG": "photos/2026/01",
		u + "/a/IMG.JPG":              "a",
	} {
		if got := dupFolder(u, in); got != want {
			t.Errorf("dupFolder(%q) = %q, want %q", in, got, want)
		}
	}
}

// Two contradictory resolves at once (keep A drop B, keep B drop A): one wins, the other is
// refused, and one copy of the frame stays.
func TestDupsResolve_ConcurrentContradictory(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	for round := 0; round < 3; round++ {
		jpg, dng := e.dupPair("IMG_C"+strings.Repeat("x", round), time.Date(2026, 1, 5+round, 10, 0, 0, 0, time.UTC))
		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for i, pair := range [][2]db.PhotoAsset{{jpg, dng}, {dng, jpg}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = e.do(e.tok, "POST", "/me/photos/v1/duplicates/resolve", map[string]any{
					"keep": db.UUIDString(pair[0].ID), "delete": []string{db.UUIDString(pair[1].ID)}}).Code
			}()
		}
		close(start)
		wg.Wait()
		ok, refused := 0, 0
		for _, c := range codes {
			switch {
			case c == 200:
				ok++
			case c == 400 || c == 404: // 404: the loser names a card the winner trashed
				refused++
			}
		}
		if ok != 1 || refused != 1 {
			t.Fatalf("round %d: codes %v, want one 200 and one refusal", round, codes)
		}
		live := 0
		for _, a := range []db.PhotoAsset{jpg, dng} {
			got, _ := e.q.GetPhotoAsset(e.ctx, db.GetPhotoAssetParams{ID: a.ID, UserID: a.UserID})
			if !got.TrashedAt.Valid {
				live++
			}
		}
		if live != 1 {
			t.Fatalf("round %d: %d copies live, want 1", round, live)
		}
	}
}

// A byte copy that is not the card's representative stands for the card in a dismissal.
func TestDupsDismiss_ByteCopyID(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	taken := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	jpg, dng := e.dupPair("IMG_9", taken)
	// The DNG's byte copy: no hash of its own, so the DNG represents the content.
	twin := e.asset(e.user, assetOpts{name: "IMG_9 (1).DNG", taken: taken, size: 5000})
	if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET content_hash = 'CH9' WHERE id = ANY($1::uuid[])`,
		[]pgtype.UUID{dng.ID, twin.ID}); err != nil {
		t.Fatal(err)
	}
	if c := dismiss(e, e.tok, dng, twin); c != 400 {
		t.Fatalf("a card with its own byte copy = %d, want 400 (one content named twice)", c)
	}
	if c := dismiss(e, e.tok, jpg, twin); c != 204 {
		t.Fatalf("dismiss through the byte copy = %d, want 204", c)
	}
	if out := e.dups(); len(out.Groups) != 0 {
		t.Fatalf("groups = %+v", out.Groups)
	}
	e.assertLive(jpg, dng, twin)
}

// Deleting a card takes its hidden byte twin to the trash too (the gallery treats copies as
// one photo); the twin's favorite and albums carry over to the kept card. Pins the
// behaviour of the delete path the resolve shares.
func TestDupsResolve_HiddenTwinOfDeleted(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	taken := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	jpg, dng := e.dupPair("IMG_10", taken)
	twin := e.asset(e.user, assetOpts{name: "IMG_10 copy.DNG", taken: taken, size: 5000, hidden: true})
	if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET content_hash = 'CH10' WHERE id = ANY($1::uuid[])`,
		[]pgtype.UUID{dng.ID, twin.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET favorite = true WHERE id = $1`, twin.ID); err != nil {
		t.Fatal(err)
	}
	al, err := e.q.CreatePhotoAlbum(e.ctx, db.CreatePhotoAlbumParams{OwnerID: e.user.ID, Name: "Secret trip"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.AddPhotoAlbumItems(e.ctx, db.AddPhotoAlbumItemsParams{AlbumID: al.ID, AssetIds: []pgtype.UUID{twin.ID}, AddedBy: e.user.ID}); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := e.pool.QueryRow(e.ctx, `SELECT updated_at FROM photo_albums WHERE id = $1`, al.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.resolve(e.tok, jpg, dng); code != 200 {
		t.Fatalf("resolve = %d", code)
	}
	e.assertTrashed(dng, twin)
	e.assertLive(jpg)
	if got, _ := e.q.GetPhotoAssetByID(e.ctx, jpg.ID); !got.Favorite {
		t.Fatal("the hidden twin's favorite did not carry over")
	}
	var n int
	var after time.Time
	if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM photo_album_items WHERE album_id = $1 AND asset_id = $2`, al.ID, jpg.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("kept card not in the twin's album: %d %v", n, err)
	}
	if err := e.pool.QueryRow(e.ctx, `SELECT updated_at FROM photo_albums WHERE id = $1`, al.ID).Scan(&after); err != nil || !after.After(before) {
		t.Fatalf("album not touched: %v -> %v (%v)", before, after, err)
	}
}

// reclaimBytes counts only what the returned groups would free.
func TestDupsList_ReclaimOnlyReturnedGroups(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	e.dupPair("IMG_11", time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC))
	e.dupPair("IMG_12", time.Date(2026, 2, 5, 10, 0, 0, 0, time.UTC))
	out := e.dups()
	if len(out.Groups) != 2 {
		t.Fatalf("groups = %+v", out.Groups)
	}
	var want int64
	for _, g := range out.Groups {
		for _, a := range g.Assets[1:] {
			want += a.Bytes
		}
	}
	if out.ReclaimBytes != want || want != 10000 {
		t.Fatalf("reclaimBytes = %d, want %d (10000)", out.ReclaimBytes, want)
	}
}
