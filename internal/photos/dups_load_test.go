package photos

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// dupAsset pushes a file for uid's tree and inserts a ready, hashed card for it.
// Cards share one capture time, so two of them with equal hashes are duplicates.
func (e *env) dupAsset(uid string, uuid pgtype.UUID, folder string, name, src, contentHash string, hash uint64) pgtype.UUID {
	e.t.Helper()
	f, err := os.Open(src)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	r, err := e.fs.Push(context.Background(), uid, &folder, name, nil, "", f)
	if err != nil {
		e.t.Fatal(err)
	}
	n := r.Node
	var aid pgtype.UUID
	err = e.pool.QueryRow(context.Background(), `
		INSERT INTO photo_assets (user_id, node_id, kind, taken_at, taken_local, date_source, width, height,
		                          content_hash, thumb_state, phash, indexed_at)
		VALUES ($1, $2, 'photo', '2020-01-01T10:00:00Z', '2020-01-01 10:00:00', 'exif', 64, 48, $3, 'ready', $4, now())
		RETURNING id`, uuid, n.ID, contentHash, int64(hash)).Scan(&aid)
	if err != nil {
		e.t.Fatal(err)
	}
	return aid
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func TestLoadDups_CollapsesByteCopies(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := id(e.folder(nil, "P"))
	src := e.jpeg("a.jpg", 64)
	src2 := e.jpeg("b.jpg", 80)
	a1 := e.dupAsset(e.userID, e.uid, f, "a.jpg", src, "H1", 0)
	a2 := e.dupAsset(e.userID, e.uid, f, "a (1).jpg", src, "H1", 0)
	if string(a2.Bytes[:]) < string(a1.Bytes[:]) { // ids are random: the lower one represents
		a1 = a2
	}
	b := e.dupAsset(e.userID, e.uid, f, "b.jpg", src2, "H2", 1)

	groups, err := LoadDuplicateGroups(ctx, e.q, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("groups = %+v, want one group of 2", groups)
	}
	var coll, other DupCandidate
	for _, c := range groups[0] {
		if c.Copies == 2 {
			coll = c
		} else {
			other = c
		}
	}
	if coll.Copies != 2 || other.ID != b {
		t.Fatalf("collapsed %+v other %+v", coll, other)
	}
	if coll.ID != a1 || coll.Key != "H1" {
		t.Fatalf("representative is the lowest id: got %v want %v", coll.ID, a1)
	}
	var one int64
	if err := e.pool.QueryRow(ctx, `SELECT size FROM nodes WHERE id = (SELECT node_id FROM photo_assets WHERE id = $1)`, a1).Scan(&one); err != nil {
		t.Fatal(err)
	}
	if coll.Bytes != 2*one {
		t.Fatalf("collapsed bytes %d, want %d", coll.Bytes, 2*one)
	}
	if coll.DiskPath == "" || coll.Name == "" || other.Copies != 1 || !coll.ExifDated || coll.W != 64 || coll.H != 48 {
		t.Fatalf("fields: %+v", coll)
	}
}

func TestLoadDups_SkipsHiddenTrashedChildren(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := id(e.folder(nil, "P"))
	a := e.dupAsset(e.userID, e.uid, f, "a.jpg", e.jpeg("a.jpg", 64), "A", 0)
	hid := e.dupAsset(e.userID, e.uid, f, "h.jpg", e.jpeg("h.jpg", 65), "B", 0)
	tr := e.dupAsset(e.userID, e.uid, f, "t.jpg", e.jpeg("t.jpg", 66), "C", 0)
	kid := e.dupAsset(e.userID, e.uid, f, "k.mov", e.jpeg("k.jpg", 67), "D", 0)
	pend := e.dupAsset(e.userID, e.uid, f, "p.jpg", e.jpeg("p.jpg", 68), "E", 0)
	e.exec(`UPDATE photo_assets SET hidden = true WHERE id = $1`, hid)
	e.exec(`UPDATE photo_assets SET trashed_at = now() WHERE id = $1`, tr)
	e.exec(`UPDATE photo_assets SET parent_id = $2, role = 'live-video' WHERE id = $1`, kid, a)
	e.exec(`UPDATE photo_assets SET thumb_state = 'pending' WHERE id = $1`, pend)

	groups, err := LoadDuplicateGroups(ctx, e.q, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("hidden/trashed/child/pending cards must not group: %+v", groups)
	}

	b := e.dupAsset(e.userID, e.uid, f, "b.jpg", e.jpeg("b.jpg", 69), "F", 0)
	groups, err = LoadDuplicateGroups(ctx, e.q, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	if groups[0][0].ID != a || !groups[0][0].HasKids || groups[0][1].ID != b {
		t.Fatalf("want a (with kids) first: %+v", groups[0])
	}
}

func TestLoadDups_OtherUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tenant, _ := e.q.CreateTenant(ctx, "t2")
	u2, err := e.q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "q@x", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	f1 := id(e.folder(nil, "P"))
	fo, err := e.fs.CreateFolder(ctx, db.UUIDString(u2.ID), nil, "Q")
	if err != nil {
		t.Fatal(err)
	}
	f2 := id(fo)
	e.dupAsset(e.userID, e.uid, f1, "a.jpg", e.jpeg("a.jpg", 64), "A", 0)
	e.dupAsset(db.UUIDString(u2.ID), u2.ID, f2, "a.jpg", e.jpeg("a2.jpg", 64), "B", 0)

	for _, u := range []pgtype.UUID{e.uid, u2.ID} {
		groups, err := LoadDuplicateGroups(ctx, e.q, u)
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 0 {
			t.Fatalf("user %v: %+v", u, groups)
		}
	}
}

func TestLoadDups_Dismissed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := id(e.folder(nil, "P"))
	e.dupAsset(e.userID, e.uid, f, "a.jpg", e.jpeg("a.jpg", 64), "A", 0)
	e.dupAsset(e.userID, e.uid, f, "b.jpg", e.jpeg("b.jpg", 65), "B", 0)
	p := MakeDupPair("A", "B") // the cards' content hashes
	if err := e.q.AddDupDismissed(ctx, db.AddDupDismissedParams{UserID: e.uid, A: p[0], B: p[1]}); err != nil {
		t.Fatal(err)
	}
	groups, err := LoadDuplicateGroups(ctx, e.q, e.uid)
	if err != nil || len(groups) != 0 {
		t.Fatalf("dismissed pair grouped: %v %+v", err, groups)
	}
}

// dupCard is dupAsset with a chosen asset id (zero = random) and content hash (nil = NULL).
func (e *env) dupCard(aid pgtype.UUID, folder, name, src string, contentHash *string, hash uint64) pgtype.UUID {
	e.t.Helper()
	f, err := os.Open(src)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	r, err := e.fs.Push(context.Background(), e.userID, &folder, name, nil, "", f)
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.pool.QueryRow(context.Background(), `
		INSERT INTO photo_assets (id, user_id, node_id, kind, taken_at, taken_local, date_source, width, height,
		                          content_hash, thumb_state, phash, indexed_at)
		VALUES (coalesce($1, gen_random_uuid()), $2, $3, 'photo', '2020-01-01T10:00:00Z', '2020-01-01 10:00:00', 'exif', 64, 48,
		        $4, 'ready', $5, now())
		RETURNING id`, aid, e.uid, r.Node.ID, contentHash, int64(hash)).Scan(&aid)
	if err != nil {
		e.t.Fatal(err)
	}
	return aid
}

func strp(s string) *string { return &s }

func groupKeys(t *testing.T, e *env) [][]string {
	t.Helper()
	groups, err := LoadDuplicateGroups(context.Background(), e.q, e.uid)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, g := range groups {
		var ks []string
		for _, c := range g {
			ks = append(ks, c.Key)
		}
		out = append(out, ks)
	}
	return out
}

// «Оставить все» survives a byte copy that takes over as the card's representative, and a
// new perceptual copy brings the group back.
func TestLoadDups_DismissalByContent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := id(e.folder(nil, "P"))
	e.dupCard(pgtype.UUID{}, f, "a.jpg", e.jpeg("a.jpg", 64), strp("HA"), 0)
	e.dupCard(pgtype.UUID{}, f, "b.jpg", e.jpeg("b.jpg", 65), strp("HB"), 1)
	if g := groupKeys(t, e); len(g) != 1 {
		t.Fatalf("setup: %v", g)
	}
	p := MakeDupPair("HA", "HB")
	if err := e.q.AddDupDismissed(ctx, db.AddDupDismissedParams{UserID: e.uid, A: p[0], B: p[1]}); err != nil {
		t.Fatal(err)
	}
	low, _ := db.ParseUUID("00000000-0000-4000-8000-000000000001")
	e.dupCard(low, f, "a (1).jpg", e.jpeg("a1.jpg", 64), strp("HA"), 0)
	groups, err := LoadDuplicateGroups(ctx, e.q, e.uid)
	if err != nil || len(groups) != 0 {
		t.Fatalf("dismissed group back after the representative changed: %v %+v", err, groups)
	}
	e.dupCard(pgtype.UUID{}, f, "a.heic", e.jpeg("c.jpg", 66), strp("HC"), 2)
	groups, err = LoadDuplicateGroups(ctx, e.q, e.uid)
	if err != nil || len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("a new copy must bring a group back: %v %+v", err, groups)
	}
	// Complete linkage keeps HA and HB apart; HC pairs with the first of them.
	keys := map[string]bool{groups[0][0].Key: true, groups[0][1].Key: true}
	if !keys["HC"] || keys["HA"] && keys["HB"] {
		t.Fatalf("group %v", keys)
	}
	for _, c := range groups[0] {
		if c.Key == "HA" && c.ID != low {
			t.Fatalf("HA represented by %v, want the lower id", c.ID)
		}
	}
}

func TestLoadDups_NullContentHashStandsAlone(t *testing.T) {
	e := newEnv(t)
	f := id(e.folder(nil, "P"))
	a := e.dupCard(pgtype.UUID{}, f, "a.jpg", e.jpeg("a.jpg", 64), nil, 0)
	b := e.dupCard(pgtype.UUID{}, f, "b.jpg", e.jpeg("b.jpg", 64), nil, 0)
	groups, err := LoadDuplicateGroups(context.Background(), e.q, e.uid)
	if err != nil || len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("%v %+v", err, groups)
	}
	for _, c := range groups[0] {
		if c.Copies != 1 || (c.ID != a && c.ID != b) || c.Key != db.UUIDString(c.ID) {
			t.Fatalf("card %+v: want its own card keyed by its id", c)
		}
	}
}

func TestLoadDups_FlatTrashedKidsCamera(t *testing.T) {
	e := newEnv(t)
	f := id(e.folder(nil, "P"))
	a := e.dupCard(pgtype.UUID{}, f, "a.jpg", e.jpeg("a.jpg", 64), strp("A"), 0)
	b := e.dupCard(pgtype.UUID{}, f, "b.jpg", e.jpeg("b.jpg", 65), strp("B"), 0)
	flat := e.dupCard(pgtype.UUID{}, f, "c.jpg", e.jpeg("c.jpg", 66), strp("C"), 0)
	kid := e.dupCard(pgtype.UUID{}, f, "a.mov", e.jpeg("k.jpg", 67), strp("K"), 0)
	e.exec(`UPDATE photo_assets SET phash_flat = true WHERE id = $1`, flat)
	e.exec(`UPDATE photo_assets SET parent_id = $2, role = 'live-video', trashed_at = now() WHERE id = $1`, kid, a)
	e.exec(`UPDATE photo_assets SET make = ' Apple ', model = 'iPhone 15' WHERE id = $1`, a)
	e.exec(`UPDATE photo_assets SET make = 'APPLE', model = 'IPHONE 15 ' WHERE id = $1`, b)
	groups, err := LoadDuplicateGroups(context.Background(), e.q, e.uid)
	if err != nil || len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("want a+b only (flat card out): %v %+v", err, groups)
	}
	for _, c := range groups[0] {
		if c.ID == flat {
			t.Fatal("flat card is a candidate")
		}
		if c.ID == a && c.HasKids {
			t.Fatal("a trashed child counts as a kid")
		}
		if c.Make != "apple" || c.Model != "iphone 15" {
			t.Fatalf("camera not normalised: %q %q", c.Make, c.Model)
		}
	}
	_ = b
}
