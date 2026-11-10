package photos

import (
	"fmt"
	"math/rand"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

var dupBase = time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)

func dupID(n byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{15: n}, Valid: true} }

// hashAt returns a hash at Hamming distance d from 0.
func hashAt(d int) uint64 {
	if d >= 64 {
		return ^uint64(0)
	}
	return (uint64(1) << uint(d)) - 1
}

func dcand(n byte, hash uint64, dt time.Duration) DupCandidate {
	return DupCandidate{
		ID: dupID(n), Key: fmt.Sprintf("k%03d", n), Kind: "photo", TakenAt: dupBase.Add(dt), ExifDated: true,
		Make: "apple", Model: "iphone 15", W: 4000, H: 3000, Hash: hash,
		Name: "IMG.jpg", Bytes: 1000, IndexedAt: dupBase,
	}
}

func TestIsDup_RawAndJpegSameShot(t *testing.T) {
	if !IsDup(dcand(1, 0, 0), dcand(2, hashAt(4), 0)) {
		t.Fatal("want dup")
	}
}

func TestIsDup_DifferentCamera(t *testing.T) {
	a, b := dcand(1, 0, 0), dcand(2, 0, 0)
	b.Make = "canon"
	if IsDup(a, b) {
		t.Fatal("different make must not be dup")
	}
}

func TestIsDup_MissingCameraOneSide(t *testing.T) {
	a, b := dcand(1, 0, 0), dcand(2, 0, 0)
	b.Make, b.Model = "", ""
	if !IsDup(a, b) {
		t.Fatal("missing make on one side: rule must not apply")
	}
	// The loader normalises make and model; the rule compares them as they are.
	if NormCamera(" Apple ") != NormCamera("APPLE") || NormCamera(" iPhone 15") != "iphone 15" {
		t.Fatal("NormCamera must ignore case and outer spaces")
	}
	a.Make, b.Make = NormCamera(" apple "), NormCamera("APPLE")
	b.Model = NormCamera("iPhone 15 ")
	if !IsDup(a, b) {
		t.Fatal("case-insensitive trimmed match expected")
	}
}

func TestIsDup_TimeTooFar(t *testing.T) {
	if IsDup(dcand(1, 0, 0), dcand(2, 0, 3*time.Second)) {
		t.Fatal("3s apart must not be dup")
	}
}

func TestIsDup_FileDatedStrict(t *testing.T) {
	a, b := dcand(1, 0, 0), dcand(2, hashAt(2), time.Hour)
	a.FileDated, a.ExifDated = true, false
	if !IsDup(a, b) {
		t.Fatal("dist 2 should be dup")
	}
	b.Hash = hashAt(3)
	if IsDup(a, b) {
		t.Fatal("dist 3 should not be dup")
	}
}

func TestIsDup_KindMismatch(t *testing.T) {
	a, b := dcand(1, 0, 0), dcand(2, 0, 0)
	b.Kind = "video"
	if IsDup(a, b) {
		t.Fatal("kind mismatch")
	}
}

func TestIsDup_VideoDuration(t *testing.T) {
	a, b := dcand(1, 0, 0), dcand(2, 0, 0)
	a.Kind, b.Kind = "video", "video"
	a.DurationMs, b.DurationMs = 10000, 10800
	if !IsDup(a, b) {
		t.Fatal("0.8s diff should be dup")
	}
	b.DurationMs = 11500
	if IsDup(a, b) {
		t.Fatal("1.5s diff should not be dup")
	}
	b.DurationMs = 0
	if !IsDup(a, b) {
		t.Fatal("zero duration skips rule")
	}
}

func TestGroup_BurstNotMerged(t *testing.T) {
	c := []DupCandidate{
		dcand(1, 0, 0),
		dcand(2, hashAt(12), 300*time.Millisecond),
		dcand(3, hashAt(12)<<20, 600*time.Millisecond),
	}
	if g := GroupDuplicates(c, nil); len(g) != 0 {
		t.Fatalf("groups=%d, want 0", len(g))
	}
	// A burst within one second that drifts slowly: neighbouring frames 5 bits apart (over
	// the bound), the ends 11 apart. Nothing may group.
	var chain []DupCandidate
	h := uint64(0)
	for i := 0; i < 3; i++ {
		chain = append(chain, dcand(byte(10+i), h, time.Duration(i)*300*time.Millisecond))
		h ^= hashAt(5) << uint(6*i) // 5 new bits each frame
	}
	chain[2].Hash ^= 1 << 63 // ends: 5 + 5 + 1 = 11 apart
	if d := Hamming(chain[0].Hash, chain[1].Hash); d != 5 {
		t.Fatalf("setup: adjacent %d", d)
	}
	if d := Hamming(chain[1].Hash, chain[2].Hash); d != 6 && d != 5 {
		t.Fatalf("setup: adjacent %d", d)
	}
	if d := Hamming(chain[0].Hash, chain[2].Hash); d != 11 {
		t.Fatalf("setup: ends %d", d)
	}
	if g := GroupDuplicates(chain, nil); len(g) != 0 {
		t.Fatalf("chain: groups=%v, want none", g)
	}
}

func TestGroup_Transitive(t *testing.T) {
	// a~b (dist 4), b~c (dist 4), a!~c (dist 8): complete linkage, no 3-group. The earliest
	// card (a) starts the group and takes b; c is left alone (its only partner is taken).
	a := dcand(1, 0, 0)
	b := dcand(2, hashAt(4), time.Second)
	c := dcand(3, hashAt(8), 2*time.Second)
	c.Hash = hashAt(4) ^ (hashAt(4) << 4) // 4 from b, 8 from a
	if Hamming(a.Hash, c.Hash) != 8 || Hamming(b.Hash, c.Hash) != 4 || !IsDup(a, b) || !IsDup(b, c) || IsDup(a, c) {
		t.Fatal("test setup")
	}
	for _, in := range [][]DupCandidate{{c, a, b}, {a, b, c}, {b, c, a}} {
		g := GroupDuplicates(in, nil)
		if len(g) != 1 || len(g[0]) != 2 {
			t.Fatalf("got %v", g)
		}
		ids := map[pgtype.UUID]bool{g[0][0].ID: true, g[0][1].ID: true}
		if !ids[a.ID] || !ids[b.ID] {
			t.Fatalf("want the pair a+b, got %v", g[0])
		}
	}
}

func TestGroup_CompleteLinkage(t *testing.T) {
	// Four copies of one frame, pairwise close, plus one card close to only one of them:
	// the four group, the straggler stays out.
	var c []DupCandidate
	for i := 0; i < 4; i++ {
		c = append(c, dcand(byte(i+1), uint64(1)<<uint(i), 0)) // pairwise 2 apart
	}
	c = append(c, dcand(9, uint64(1)|hashAt(4)<<10, time.Second)) // 4 from c[0], 5–6 from the rest
	g := GroupDuplicates(c, nil)
	if len(g) != 1 || len(g[0]) != 4 {
		t.Fatalf("got %v", g)
	}
	for _, x := range g[0] {
		if x.ID == dupID(9) {
			t.Fatal("straggler joined")
		}
	}
}

func TestGroup_FileDated(t *testing.T) {
	// A copy whose date comes from the file (a re-saved export) matches its frame at any
	// time, within the strict bound only.
	orig := dcand(1, 0x00ff_00ff_00ff_00ff, 0)
	copy1 := dcand(2, orig.Hash^hashAt(2), 400*24*time.Hour)
	copy1.FileDated, copy1.ExifDated = true, false
	far := dcand(3, orig.Hash^hashAt(3), 0) // 3 bits: too far for an untimed match
	far.FileDated, far.ExifDated = true, false
	other := dcand(4, ^orig.Hash, 0)
	g := GroupDuplicates([]DupCandidate{other, far, copy1, orig}, nil)
	if len(g) != 1 || len(g[0]) != 2 {
		t.Fatalf("got %v", g)
	}
	if g[0][0].ID != orig.ID || g[0][1].ID != copy1.ID {
		t.Fatalf("want orig (exif date) then the file-dated copy: %v", g[0])
	}
	// Two file-dated copies of each other, far apart in time.
	a := dcand(5, 0x1234_5678_9abc_def0, 0)
	b := dcand(6, a.Hash^1, 1000*time.Hour)
	a.FileDated, b.FileDated = true, true
	if g := GroupDuplicates([]DupCandidate{a, b}, nil); len(g) != 1 {
		t.Fatalf("filed pair: %v", g)
	}
	// The pigeonhole index must stay exact for every bound.
	for k := 0; k <= 4; k++ {
		p := DefaultDupParams
		p.MaxDistUntimed = k
		x := dcand(7, 0xdead_beef_cafe_f00d, 0)
		y := dcand(8, x.Hash^hashAt(k)<<uint(60-k), time.Hour) // k bits, all in the top chunk
		z := dcand(9, x.Hash^hashAt(k+1), time.Hour)
		x.FileDated, y.FileDated, z.FileDated = true, true, true
		y.Key, z.Key = "y", "z"
		g := GroupDuplicatesWith(p, []DupCandidate{x, y}, nil)
		if len(g) != 1 {
			t.Fatalf("k=%d: %d bits apart not grouped", k, k)
		}
		if g := GroupDuplicatesWith(p, []DupCandidate{x, z}, nil); len(g) != 0 {
			t.Fatalf("k=%d: %d bits apart grouped", k, k+1)
		}
	}
}

func TestGroup_Deterministic(t *testing.T) {
	c := randomCandidates(3000, 7)
	// Cluster some cards so there are groups (and chains) to choose between.
	for i := 0; i+3 < len(c); i += 40 {
		for j := 1; j <= 3; j++ {
			c[i+j].TakenAt = c[i].TakenAt.Add(time.Duration(j) * 500 * time.Millisecond)
			c[i+j].Hash = c[i].Hash ^ hashAt(2*j)
		}
	}
	want := fmt.Sprint(groupIDs(GroupDuplicates(c, nil)))
	r := rand.New(rand.NewSource(1))
	for n := 0; n < 5; n++ {
		d := append([]DupCandidate(nil), c...)
		r.Shuffle(len(d), func(i, j int) { d[i], d[j] = d[j], d[i] })
		if got := fmt.Sprint(groupIDs(GroupDuplicates(d, nil))); got != want {
			t.Fatal("groups depend on the input order")
		}
	}
	if want == "[]" {
		t.Fatal("setup: no groups")
	}
}

func groupIDs(g [][]DupCandidate) [][]pgtype.UUID {
	out := make([][]pgtype.UUID, len(g))
	for i, x := range g {
		for _, c := range x {
			out[i] = append(out[i], c.ID)
		}
	}
	return out
}

// randomCandidates is a library of n cards: random hashes, times over five years, one in
// ten dated from its file.
func randomCandidates(n int, seed int64) []DupCandidate {
	r := rand.New(rand.NewSource(seed))
	span := int64(5 * 365 * 24 * time.Hour)
	c := make([]DupCandidate, n)
	for i := range c {
		var id pgtype.UUID
		r.Read(id.Bytes[:])
		id.Valid = true
		c[i] = DupCandidate{
			ID: id, Key: fmt.Sprint(i), Kind: "photo", TakenAt: dupBase.Add(time.Duration(r.Int63n(span))),
			ExifDated: true, Make: "apple", Model: "iphone 15", W: 4000, H: 3000, Hash: r.Uint64(),
			Name: "IMG.jpg", Bytes: 1000, IndexedAt: dupBase,
		}
		if i%10 == 0 {
			c[i].FileDated, c[i].ExifDated = true, false
		}
		if i%7 == 0 {
			c[i].Kind = "video"
		}
	}
	return c
}

func BenchmarkGroupDuplicates(b *testing.B) {
	c := randomCandidates(90_000, 42)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GroupDuplicates(c, nil)
	}
}

func TestGroup_LargeLibraryFast(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	c := randomCandidates(90_000, 42)
	start := time.Now()
	GroupDuplicates(c, nil)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("90k candidates grouped in %v, want < 2s", d)
	}
}

func TestGroup_Dismissed(t *testing.T) {
	a, b, c := dcand(1, 0, 0), dcand(2, 0, 0), dcand(3, 0, 0)
	d := map[DupPair]bool{
		MakeDupPair(a.Key, b.Key): true,
		MakeDupPair(b.Key, c.Key): true,
		MakeDupPair(a.Key, c.Key): true,
	}
	if g := GroupDuplicates([]DupCandidate{a, b, c}, d); len(g) != 0 {
		t.Fatalf("groups=%d, want 0", len(g))
	}
	// A new copy e of the frame: a group comes back. Complete linkage keeps the dismissed
	// pairs apart, so e pairs with the first card (a); b and c stay out.
	e := dcand(4, 0, 0)
	g := GroupDuplicates([]DupCandidate{a, b, c, e}, d)
	if len(g) != 1 || len(g[0]) != 2 || g[0][0].ID != a.ID || g[0][1].ID != e.ID {
		t.Fatalf("got %v", g)
	}
	// Dismissed by content key: another card with a's key (its byte copy taking over as the
	// representative) is still dismissed with b.
	a2 := dcand(5, 0, 0)
	a2.Key = a.Key
	if g := GroupDuplicates([]DupCandidate{a2, b}, d); len(g) != 0 {
		t.Fatalf("representative changed: %v", g)
	}
}

func TestGroup_OrderNewestFirst(t *testing.T) {
	a1, a2 := dcand(1, 0, 0), dcand(2, 0, 0)
	b1, b2 := dcand(3, hashAt(30), time.Hour), dcand(4, hashAt(30), time.Hour)
	g := GroupDuplicates([]DupCandidate{a1, a2, b1, b2}, nil)
	if len(g) != 2 || !g[0][0].TakenAt.After(g[1][0].TakenAt) {
		t.Fatalf("got %v", g)
	}
}

func TestRankKeep_JpegOverDng(t *testing.T) {
	dng := dcand(1, 0, 0)
	dng.Name, dng.Bytes = "IMG_1170.DNG", 28<<20
	jpg := dcand(2, 0, 0)
	jpg.Name, jpg.Bytes = "IMG_1170 (1).jpeg", 3<<20
	g := []DupCandidate{dng, jpg}
	RankKeep(g)
	if g[0].ID != jpg.ID {
		t.Fatalf("first=%s", g[0].Name)
	}
}

func TestRankKeep_KidsFirst(t *testing.T) {
	heic := dcand(1, 0, 0)
	heic.Name, heic.HasKids = "a.heic", true
	jpg := dcand(2, 0, 0)
	jpg.W, jpg.H = 8000, 6000
	g := []DupCandidate{jpg, heic}
	RankKeep(g)
	if g[0].ID != heic.ID {
		t.Fatal("kids must win")
	}
}

func TestRankKeep_Order(t *testing.T) {
	type tc struct {
		name          string
		better, worse func(*DupCandidate)
	}
	nop := func(*DupCandidate) {}
	cases := []tc{
		{"not raw", func(c *DupCandidate) { c.Name = "a.jpg" }, func(c *DupCandidate) { c.Name = "a.dng" }},
		{"resolution", func(c *DupCandidate) { c.W, c.H = 5000, 4000 }, nop},
		{"exif", nop, func(c *DupCandidate) { c.ExifDated = false }},
		{"copy suffix", func(c *DupCandidate) { c.Name = "a.jpg" }, func(c *DupCandidate) { c.Name = "a copy.jpg" }},
		{"size", func(c *DupCandidate) { c.Bytes = 5000 }, nop},
		{"indexed", nop, func(c *DupCandidate) { c.IndexedAt = dupBase.Add(time.Hour) }},
	}
	for _, k := range cases {
		better, worse := dcand(1, 0, 0), dcand(2, 0, 0)
		k.better(&better)
		k.worse(&worse)
		g := []DupCandidate{worse, better}
		RankKeep(g)
		if g[0].ID != better.ID {
			t.Errorf("%s: wrong winner", k.name)
		}
	}
}

func TestRankKeep_IDTieBreak(t *testing.T) {
	lo := DupCandidate{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, Name: "a.jpg"}
	hi := DupCandidate{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, Name: "b.jpg"}
	for _, g := range [][]DupCandidate{{lo, hi}, {hi, lo}} {
		RankKeep(g)
		if g[0].ID != lo.ID {
			t.Fatalf("identical candidates: smaller id must be first, got %v", g[0].ID)
		}
	}
}

// The headline case: a DNG that records its UTC offset and the JPEG a converter made of
// it without the offset tags. taken_at is the true instant for the first and the wall
// clock labelled UTC for the second, hours apart; the wall clock at the place of capture
// (taken_local) is the same.
func TestGroup_SameShotWithAndWithoutTZOffset(t *testing.T) {
	local := time.Date(2024, 5, 1, 18, 0, 0, 0, time.UTC) // 18:00 on the camera's clock
	dng := dcand(1, 0, 0)
	dng.TakenAt, dng.TakenLocal = local.Add(-6*time.Hour), local // offset +06:00 known
	jpg := dcand(2, hashAt(3), 0)
	jpg.TakenAt, jpg.TakenLocal = local, local // no offset: wall clock labelled UTC
	if !IsDup(dng, jpg) {
		t.Fatal("IsDup: the same shot with and without a recorded offset is not a duplicate")
	}
	if g := GroupDuplicates([]DupCandidate{dng, jpg}, nil); len(g) != 1 || len(g[0]) != 2 {
		t.Fatalf("groups = %v, want one group of two", g)
	}
	// Different moments stay apart whatever their instants say.
	other := dcand(3, hashAt(2), 0)
	other.TakenAt, other.TakenLocal = dng.TakenAt, local.Add(-6*time.Hour)
	if IsDup(dng, other) {
		t.Fatal("IsDup: shots six hours apart on the camera's clock are duplicates")
	}
}

// Real hashes are skewed: a flat band (sky, a screenshot's margin) zeroes one chunk of the
// hash, so thousands of cards without EXIF dates land in one bucket of the index. Every
// pair tried used to be remembered in a map — millions of entries, hundreds of megabytes
// for one request.
func TestGroup_SkewedHashesStayWithinMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	r := rand.New(rand.NewSource(7))
	c := make([]DupCandidate, 3000)
	for i := range c {
		var id pgtype.UUID
		r.Read(id.Bytes[:])
		id.Valid = true
		c[i] = DupCandidate{
			ID: id, Key: fmt.Sprint(i), Kind: "photo", TakenAt: dupBase.Add(time.Duration(i) * time.Hour),
			FileDated: true, W: 1170, H: 2532, Hash: r.Uint64() &^ (1<<22 - 1), // the low chunk is all zeros
			Name: "IMG.png", Bytes: 1000, IndexedAt: dupBase,
		}
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	GroupDuplicates(c, nil)
	runtime.ReadMemStats(&after)
	if mb := (after.TotalAlloc - before.TotalAlloc) >> 20; mb > 32 {
		t.Fatalf("grouping 3000 undated cards with a shared hash chunk allocated %d MiB", mb)
	}
}

// The index of undated cards must find exactly the pairs a comparison of every card with
// every other finds — also when most hashes share a chunk and near-duplicates differ in
// any one or two bits.
func TestDupEdges_CrowdedBucketsMatchBruteForce(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	var c []DupCandidate
	add := func(h uint64, filed bool) {
		var id pgtype.UUID
		r.Read(id.Bytes[:])
		id.Valid = true
		c = append(c, DupCandidate{
			ID: id, Key: fmt.Sprint(len(c)), Kind: "photo", TakenAt: dupBase.Add(time.Duration(len(c)) * 100 * time.Hour),
			FileDated: filed, ExifDated: !filed, W: 1170, H: 2532, Hash: h, Name: "IMG.png", Bytes: 1000, IndexedAt: dupBase,
		})
	}
	for i := 0; i < 600; i++ {
		h := r.Uint64()
		switch i % 3 { // each third shares one of the three chunks
		case 0:
			h &^= 1<<21 - 1
		case 1:
			h &^= (1<<21 - 1) << 21
		case 2:
			h &^= (1<<22 - 1) << 42
		}
		add(h, i%4 != 0)
		for n := 0; n < i%4; n++ { // copies up to three bits away, anywhere in the hash
			near := h
			for b := 0; b <= n; b++ {
				near ^= 1 << uint(r.Intn(64))
			}
			add(near, n%2 == 0)
		}
	}

	p := DefaultDupParams
	want := map[[2]int]bool{}
	for i := range c {
		for j := i + 1; j < len(c); j++ {
			if IsDupWith(p, c[i], c[j]) {
				want[[2]int{i, j}] = true
			}
		}
	}
	got := map[[2]int]int{}
	for i, js := range dupEdges(p, c) {
		for _, j := range js {
			if i < j {
				got[[2]int{i, j}]++
			}
		}
	}
	if len(want) < 100 {
		t.Fatalf("the fixture has only %d duplicate pairs", len(want))
	}
	for k := range want {
		if got[k] == 0 {
			t.Errorf("pair %v (hashes %016x, %016x) was not found", k, c[k[0]].Hash, c[k[1]].Hash)
		}
	}
	for k, n := range got {
		if !want[k] {
			t.Errorf("pair %v is not a duplicate", k)
		}
		if n != 1 {
			t.Errorf("pair %v was added %d times", k, n)
		}
	}
}
