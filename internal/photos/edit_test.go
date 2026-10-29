package photos

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

func (e *env) editor() *Editor { return NewEditor(e.q, e.fs, e.root, e.tools, e.ix) }

func (e *env) disk(n db.Node) string { return filepath.Join(e.root, n.DiskPath.String) }

// exifOf reads tags of a file with `exiftool -n -G1 -json`.
func (e *env) exifOf(path string) map[string]any {
	e.t.Helper()
	out, err := exec.Command(e.tools.Exiftool, "-n", "-G1", "-json", path).Output()
	if err != nil {
		e.t.Fatal(err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(out, &arr); err != nil || len(arr) == 0 {
		e.t.Fatalf("exiftool json: %v", err)
	}
	return arr[0]
}

func (e *env) refresh(n db.Node) db.Node {
	e.t.Helper()
	r, err := e.q.GetNode(context.Background(), n.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func tm(y int, mo time.Month, d, h, mi int, loc *time.Location) *time.Time {
	t := time.Date(y, mo, d, h, mi, 0, 0, loc)
	return &t
}

func ip(v int) *int         { return &v }
func fp(v float64) *float64 { return &v }

func TestEdit_JPEGDateWrittenAndVersioned(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	n := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64, "-DateTimeOriginal=2019:07:14 19:42:00", "-OffsetTimeOriginal=+06:00"))
	e.catchUp()
	a := e.mustAsset(n)
	oldSize := n.Size.Int64

	res, err := e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a.ID)}, Edit{
		SetTime: tm(2020, 1, 2, 3, 4, time.UTC), TZOffsetMin: ip(120)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updated) != 1 || len(res.Conflicts) != 0 || res.VersionBytes != oldSize {
		t.Fatalf("result %+v (old size %d)", res, oldSize)
	}
	tags := e.exifOf(e.disk(n))
	if tags["ExifIFD:DateTimeOriginal"] != "2020:01:02 05:04:00" || tags["ExifIFD:OffsetTimeOriginal"] != "+02:00" {
		t.Fatalf("file tags: %v %v", tags["ExifIFD:DateTimeOriginal"], tags["ExifIFD:OffsetTimeOriginal"])
	}
	b := e.mustAsset(n)
	if b.DateSource != "edit" || !b.TakenAt.Time.Equal(time.Date(2020, 1, 2, 3, 4, 0, 0, time.UTC)) ||
		!b.TakenLocal.Time.Equal(time.Date(2020, 1, 2, 5, 4, 0, 0, time.UTC)) || b.TzOffsetMin.Int32 != 120 {
		t.Fatalf("asset %+v", b)
	}
	vs, err := e.q.ListFileVersions(ctx, n.ID)
	if err != nil || len(vs) == 0 {
		t.Fatalf("no file version kept: %v %d", err, len(vs))
	}
	// The tracker's pass over the new version must not undo the edit.
	e.catchUp()
	if c := e.mustAsset(n); c.DateSource != "edit" || !c.TakenAt.Time.Equal(b.TakenAt.Time) {
		t.Fatalf("catch-up reverted the edit: %+v", c)
	}
	// A forced re-read of the file reports the same instant and keeps the label.
	if err := e.ix.indexNode(ctx, n.ID, true); err != nil {
		t.Fatal(err)
	}
	if c := e.mustAsset(n); c.DateSource != "edit" {
		t.Fatalf("re-read dropped the label: %+v", c)
	}
}

func TestEdit_HEICPlace(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	p := filepath.Join(t.TempDir(), "a.heic")
	run(t, e.tools.Vips, "black", p, "64", "48")
	n := e.push(&sid, "a.heic", p)
	e.catchUp()
	a := e.mustAsset(n)

	res, err := e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a.ID)}, Edit{SetPlace: true, Lat: fp(-33.8688), Lon: fp(151.2093)})
	if err != nil || len(res.Updated) != 1 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	tags := e.exifOf(e.disk(n))
	if tags["GPS:GPSLatitudeRef"] != "S" || tags["GPS:GPSLongitudeRef"] != "E" {
		t.Fatalf("refs %v %v", tags["GPS:GPSLatitudeRef"], tags["GPS:GPSLongitudeRef"])
	}
	b := e.mustAsset(n)
	if !b.Lat.Valid || b.Lat.Float64 < -33.87 || b.Lat.Float64 > -33.86 || b.Lon.Float64 < 151.2 || b.Lon.Float64 > 151.22 {
		t.Fatalf("asset place %v %v", b.Lat, b.Lon)
	}
	if b.DateSource == "edit" {
		t.Fatal("a place edit must not claim the date")
	}
}

func TestEdit_VideoGoesToSidecar(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	mp4 := e.mov("v.mov")
	before, _ := os.ReadFile(mp4)
	n := e.push(&sid, "v.mov", mp4)
	e.catchUp()
	a := e.mustAsset(n)

	res, err := e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a.ID)}, Edit{
		SetTime: tm(2018, 3, 4, 5, 6, time.UTC), TZOffsetMin: ip(-300), SetPlace: true, Lat: fp(10.5), Lon: fp(-20.25)})
	if err != nil || len(res.Updated) != 1 || res.VersionBytes != 0 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	after, _ := os.ReadFile(e.disk(n))
	if !bytes.Equal(before, after) {
		t.Fatal("the video was modified")
	}
	if _, err := os.Stat(e.disk(n) + ".xmp"); err != nil {
		t.Fatalf("no sidecar: %v", err)
	}
	check := func(stage string) {
		t.Helper()
		b := e.mustAsset(n)
		if b.DateSource != "edit" || !b.TakenAt.Time.Equal(time.Date(2018, 3, 4, 5, 6, 0, 0, time.UTC)) ||
			!b.TakenLocal.Time.Equal(time.Date(2018, 3, 4, 0, 6, 0, 0, time.UTC)) || b.TzOffsetMin.Int32 != -300 ||
			!b.Lat.Valid || b.Lat.Float64 != 10.5 || b.Lon.Float64 != -20.25 {
			t.Fatalf("%s: asset %+v", stage, b)
		}
	}
	check("after apply")
	e.catchUp() // the sidecar's own change re-reads the video
	check("after catch-up")
	// A plain re-read takes the date from the sidecar.
	m, err := e.ix.exif.Read(ctx, e.disk(n))
	if err != nil || !m.DateFromXMP {
		t.Fatalf("re-read: %v %+v", err, m)
	}

	// An existing <stem>.xmp is updated instead of a second sidecar.
	// Distinct content: a copy of v.mov would be edited with the card.
	n2 := e.push(&sid, "w.mov", e.mov("w.mov", "-Keys:Title=w"))
	side := filepath.Join(t.TempDir(), "w.xmp")
	if err := os.WriteFile(side, []byte(emptyXMP), 0o600); err != nil {
		t.Fatal(err)
	}
	sn := e.push(&sid, "w.xmp", side)
	e.catchUp()
	a2 := e.mustAsset(n2)
	res, err = e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a2.ID)}, Edit{SetTime: tm(2001, 2, 3, 4, 5, time.UTC)})
	if err != nil || len(res.Updated) != 1 || res.VersionBytes != sn.Size.Int64 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	if _, err := os.Stat(e.disk(n2) + ".xmp"); err == nil {
		t.Fatal("a second sidecar was created")
	}
	if b := e.mustAsset(n2); b.DateSource != "edit" || b.TakenAt.Time.Year() != 2001 {
		t.Fatalf("asset %+v", b)
	}
}

// c2paPNG writes a 2×2 PNG with a C2PA manifest box (caBX) ahead of the pixels, as
// ChatGPT images carry. exiftool writes EXIF into such a file but does not read it back
// ("IFD0 pointer references previous JUMBFBox directory").
func c2paPNG(t *testing.T) string {
	t.Helper()
	chunk := func(typ string, data []byte) []byte {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
		return b.Bytes()
	}
	box := func(typ string, data []byte) []byte {
		b := binary.BigEndian.AppendUint32(nil, uint32(8+len(data)))
		return append(append(b, typ...), data...)
	}
	uuid, _ := hex.DecodeString("6332706100110010800000aa00389b71")
	jumb := box("jumb", box("jumd", append(append(uuid, 3), "c2pa\x00"...)))
	var pix bytes.Buffer
	z := zlib.NewWriter(&pix)
	_, _ = z.Write([]byte{0, 128, 128, 128, 128, 128, 128, 0, 128, 128, 128, 128, 128, 128})
	_ = z.Close()
	var png bytes.Buffer
	png.WriteString("\x89PNG\r\n\x1a\n")
	png.Write(chunk("IHDR", []byte{0, 0, 0, 2, 0, 0, 0, 2, 8, 2, 0, 0, 0}))
	png.Write(chunk("caBX", jumb))
	png.Write(chunk("IDAT", pix.Bytes()))
	png.Write(chunk("IEND", nil))
	p := filepath.Join(t.TempDir(), "c2pa.png")
	if err := os.WriteFile(p, png.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A file whose written tags do not read back keeps its bytes; the edit goes to a sidecar.
func TestEdit_UnreadableWriteGoesToSidecar(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	p := c2paPNG(t)
	before, _ := os.ReadFile(p)
	n := e.push(&sid, "image_gpt.PNG", p)
	e.catchUp()
	a := e.mustAsset(n)

	for _, when := range []*time.Time{tm(2020, 1, 2, 3, 4, time.UTC), tm(2021, 5, 6, 7, 8, time.UTC)} {
		res, err := e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a.ID)}, Edit{SetTime: when})
		if err != nil || len(res.Updated) != 1 {
			t.Fatalf("apply: %v %+v", err, res)
		}
		if b := e.mustAsset(n); b.DateSource != "edit" || !b.TakenAt.Time.Equal(*when) {
			t.Fatalf("asset taken %v, want %v", b.TakenAt.Time, *when)
		}
		e.catchUp()
		if b := e.mustAsset(n); !b.TakenAt.Time.Equal(*when) {
			t.Fatalf("after catch-up: taken %v, want %v", b.TakenAt.Time, *when)
		}
	}
	after, _ := os.ReadFile(e.disk(n))
	if !bytes.Equal(before, after) {
		t.Fatal("the file was rewritten with tags nobody reads")
	}
	if _, err := os.Stat(e.disk(n) + ".xmp"); err != nil {
		t.Fatalf("no sidecar: %v", err)
	}
}

func TestEdit_ConcurrentChangeConflict(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	n := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64, "-DateTimeOriginal=2019:07:14 19:42:00"))
	e.catchUp()
	a := e.mustAsset(n)

	newer := e.jpeg("b.jpg", 80, "-DateTimeOriginal=2022:02:02 02:02:02")
	want, _ := os.ReadFile(newer)
	beforeEditCommit = func() {
		e.push(&sid, "a.jpg", newer)
	}
	defer func() { beforeEditCommit = func() {} }()

	id := db.UUIDString(a.ID)
	kids := func() int {
		l, err := e.q.ListChildren(ctx, db.ListChildrenParams{UserID: e.uid, ParentID: src.ID})
		if err != nil {
			t.Fatal(err)
		}
		return len(l)
	}
	nodesBefore := kids()
	res, err := e.editor().Apply(ctx, e.userID, []string{id}, Edit{SetTime: tm(2020, 1, 2, 3, 4, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updated) != 0 || len(res.Conflicts) != 1 || res.Conflicts[0] != id {
		t.Fatalf("result %+v", res)
	}
	got, _ := os.ReadFile(e.disk(n))
	if !bytes.Equal(got, want) {
		t.Fatal("the concurrent content was overwritten")
	}
	if kids() != nodesBefore {
		t.Fatal("a conflict copy was left in the folder")
	}
	if b := e.mustAsset(n); b.DateSource == "edit" {
		t.Fatalf("a conflicted edit reached the row: %+v", b)
	}
}

func TestEdit_ShiftHours(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	n1 := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64, "-DateTimeOriginal=2019:07:14 19:42:00", "-OffsetTimeOriginal=+06:00"))
	n2 := e.push(&sid, "b.jpg", e.jpeg("b.jpg", 64, "-DateTimeOriginal=2020:12:31 23:30:00"))
	e.catchUp()
	ids := []string{db.UUIDString(e.mustAsset(n1).ID), db.UUIDString(e.mustAsset(n2).ID)}

	res, err := e.editor().Apply(ctx, e.userID, ids, Edit{ShiftMin: -3 * 60})
	if err != nil || len(res.Updated) != 2 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	a := e.mustAsset(n1)
	if !a.TakenLocal.Time.Equal(time.Date(2019, 7, 14, 16, 42, 0, 0, time.UTC)) || a.TzOffsetMin.Int32 != 360 ||
		!a.TakenAt.Time.Equal(time.Date(2019, 7, 14, 10, 42, 0, 0, time.UTC)) {
		t.Fatalf("a: %+v", a)
	}
	b := e.mustAsset(n2)
	if !b.TakenLocal.Time.Equal(time.Date(2020, 12, 31, 20, 30, 0, 0, time.UTC)) || b.TzOffsetMin.Valid {
		t.Fatalf("b: %+v", b)
	}
	// Both together are refused.
	if _, err := e.editor().Apply(ctx, e.userID, ids, Edit{ShiftMin: 5, SetTime: tm(2020, 1, 1, 0, 0, time.UTC)}); err == nil {
		t.Fatal("shift and a new time were accepted together")
	}
}

func TestEdit_ClearPlace(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	n := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64, "-DateTimeOriginal=2019:07:14 19:42:00",
		"-GPSLatitude=42.6475", "-GPSLatitudeRef=N", "-GPSLongitude=77.0820", "-GPSLongitudeRef=E", "-GPSAltitude=650"))
	e.catchUp()
	a := e.mustAsset(n)
	if !a.Lat.Valid {
		t.Fatal("fixture has no position")
	}
	res, err := e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a.ID)}, Edit{SetPlace: true})
	if err != nil || len(res.Updated) != 1 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	for k := range e.exifOf(e.disk(n)) {
		if len(k) > 4 && k[:4] == "GPS:" && k != "GPS:GPSVersionID" {
			t.Fatalf("GPS tag %s left in the file", k)
		}
	}
	if b := e.mustAsset(n); b.Lat.Valid || b.Lon.Valid || b.PlaceID.Valid {
		t.Fatalf("asset still has a place: %+v", b)
	}
}

func TestEdit_ClearPlaceNothingToClear(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	j := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64, "-DateTimeOriginal=2019:07:14 19:42:00"))
	v := e.push(&sid, "v.mov", e.mov("v.mov"))
	e.catchUp()
	ids := []string{db.UUIDString(e.mustAsset(j).ID), db.UUIDString(e.mustAsset(v).ID)}
	res, err := e.editor().Apply(ctx, e.userID, ids, Edit{SetPlace: true})
	if err != nil || len(res.Updated) != 2 || len(res.Failed) != 0 || res.VersionBytes != 0 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	if _, err := os.Stat(e.disk(v) + ".xmp"); err == nil {
		t.Fatal("a sidecar was made for nothing")
	}
	if vs, _ := e.q.ListFileVersions(ctx, j.ID); len(vs) != 0 {
		t.Fatal("an unchanged file got a new version")
	}
}

func TestEdit_ClearPlaceEmbeddedInVideo(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	mov := e.mov("v.mov", "-Keys:GPSCoordinates=+42.6475+077.0820/")
	before, _ := os.ReadFile(mov)
	n := e.push(&sid, "v.mov", mov)
	e.catchUp()
	a := e.mustAsset(n)
	if !a.Lat.Valid {
		t.Fatal("fixture has no embedded position")
	}
	res, err := e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a.ID)}, Edit{SetPlace: true})
	if err != nil || len(res.Updated) != 1 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	after, _ := os.ReadFile(e.disk(n))
	if !bytes.Equal(before, after) {
		t.Fatal("the video was modified")
	}
	cleared := func(stage string) {
		t.Helper()
		if b := e.mustAsset(n); b.Lat.Valid || b.Lon.Valid || b.PlaceID.Valid || !b.PlaceCleared {
			t.Fatalf("%s: %+v", stage, b)
		}
	}
	cleared("after apply")
	e.catchUp()
	if err := e.ix.indexNode(ctx, n.ID, true); err != nil {
		t.Fatal(err)
	}
	cleared("after re-read")

	// A new place resets the flag.
	res, err = e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a.ID)}, Edit{SetPlace: true, Lat: fp(1.5), Lon: fp(2.5)})
	if err != nil || len(res.Updated) != 1 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	if b := e.mustAsset(n); b.PlaceCleared || !b.Lat.Valid || b.Lat.Float64 != 1.5 {
		t.Fatalf("after new place: %+v", b)
	}
}

func TestEdit_TimezoneCleared(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	n := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64, "-DateTimeOriginal=2019:07:14 19:42:00",
		"-OffsetTimeOriginal=+06:00", "-OffsetTime=+06:00", "-OffsetTimeDigitized=+06:00"))
	e.catchUp()
	a := e.mustAsset(n)
	if !a.TzOffsetMin.Valid {
		t.Fatal("fixture has no offset")
	}
	res, err := e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a.ID)}, Edit{SetTime: tm(2020, 5, 6, 7, 8, time.UTC)})
	if err != nil || len(res.Updated) != 1 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	b := e.mustAsset(n)
	if b.TzOffsetMin.Valid || !b.TakenAt.Time.Equal(time.Date(2020, 5, 6, 7, 8, 0, 0, time.UTC)) {
		t.Fatalf("asset %+v", b)
	}
	m, err := e.ix.exif.Read(ctx, e.disk(n))
	if err != nil || m.TZOffsetMin != nil || !m.TakenAt.Equal(time.Date(2020, 5, 6, 7, 8, 0, 0, time.UTC)) {
		t.Fatalf("re-read: %v %+v", err, m)
	}
}

// A card whose second file changes after the first was published is reported as
// partial, and the row of the written file follows its file.
func TestEdit_PartialCard(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	j := e.push(&sid, "a.jpg", e.jpeg("a.jpg", 64, "-DateTimeOriginal=2019:07:14 19:42:00"))
	v := e.push(&sid, "a.jpg.mov", e.mov("a.mov"))
	e.catchUp()
	ja, va := e.mustAsset(j), e.mustAsset(v)
	if err := e.q.SetPhotoParent(ctx, db.SetPhotoParentParams{ID: va.ID, ParentID: ja.ID, Role: pgtype.Text{String: "live-video", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	side := filepath.Join(t.TempDir(), "s.xmp")
	if err := os.WriteFile(side, []byte(emptyXMP), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeEditCommit = func() {
		calls++
		if calls == 2 {
			e.push(&sid, "a.jpg.mov.xmp", side) // somebody made the sidecar first
		}
	}
	defer func() { beforeEditCommit = func() {} }()
	res, err := e.editor().Apply(ctx, e.userID, []string{db.UUIDString(ja.ID)}, Edit{SetTime: tm(2020, 1, 2, 3, 4, time.UTC)})
	cid := db.UUIDString(ja.ID)
	if err != nil || len(res.Partial) != 1 || res.Partial[0] != cid || len(res.Updated) != 0 || len(res.Conflicts) != 0 {
		t.Fatalf("apply: %v %+v", err, res)
	}
	if b := e.mustAsset(j); b.DateSource != "edit" || b.TakenAt.Time.Year() != 2020 {
		t.Fatalf("written file's row: %+v", b)
	}
}

// A place edit of a file without a date inside keeps the card where it was: the date
// comes from the node's modification time, which the edit must not reset to "now".
func TestEdit_PlaceKeepsFileDate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := e.source("Photos")
	sid := id(src)
	when := time.Date(2014, 5, 6, 7, 8, 9, 0, time.UTC)
	n := e.pushMeta(&sid, "scan.jpg", e.jpeg("scan.jpg", 64), storage.PushMeta{ModifiedAt: when})
	e.catchUp()
	a := e.mustAsset(n)
	if a.DateSource != "file" || !a.TakenAt.Time.Equal(when) {
		t.Fatalf("before: %s %v", a.DateSource, a.TakenAt.Time)
	}
	res, err := e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a.ID)}, Edit{SetPlace: true, Lat: fp(42.6475), Lon: fp(77.082)})
	if err != nil || len(res.Updated) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	b := e.mustAsset(n)
	if b.DateSource != "file" || !b.TakenAt.Time.Equal(when) {
		t.Fatalf("after a place edit: %s %v, want file %v", b.DateSource, b.TakenAt.Time, when)
	}
	if !b.Lat.Valid || b.Lat.Float64 < 42.64 || b.Lat.Float64 > 42.65 {
		t.Fatalf("place not stored: %+v", b.Lat)
	}
	if got := e.refresh(n); !got.ModifiedAt.Time.Equal(when) || got.Version == n.Version {
		t.Fatalf("node: modified %v version %d (was %d)", got.ModifiedAt.Time, got.Version, n.Version)
	}
}

// A date edit of a card reaches its copies in other folders: they stay one card with the
// new date instead of the twin coming back with the old one.
func TestEdit_CopiesFollowTheCard(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s1, s2 := e.source("Phone"), e.source("Archive")
	id1, id2 := id(s1), id(s2)
	file := e.jpeg("a.jpg", 64, "-DateTimeOriginal=2019:07:14 19:42:00", "-OffsetTimeOriginal=+06:00")
	n1 := e.push(&id1, "IMG_1.jpg", file)
	n2 := e.push(&id2, "IMG_1.jpg", file)
	other := e.push(&id2, "other.jpg", e.jpeg("o.jpg", 70, "-DateTimeOriginal=2019:07:14 19:42:00"))
	e.catchUp()
	a1, a2, ao := e.mustAsset(n1), e.mustAsset(n2), e.mustAsset(other)
	if a1.ContentHash != a2.ContentHash {
		t.Fatal("fixture: the copies differ")
	}
	res, err := e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a1.ID)}, Edit{ShiftMin: 120})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updated) != 1 || res.Updated[0] != db.UUIDString(a1.ID) || len(res.Failed)+len(res.Conflicts)+len(res.Partial) != 0 {
		t.Fatalf("result %+v", res)
	}
	want := time.Date(2019, 7, 14, 21, 42, 0, 0, time.UTC)
	b1, b2 := e.mustAsset(n1), e.mustAsset(n2)
	for _, b := range []db.PhotoAsset{b1, b2} {
		if !b.TakenLocal.Time.Equal(want) || b.DateSource != "edit" {
			t.Fatalf("asset %s: %v %s, want %v edit", db.UUIDString(b.ID), b.TakenLocal.Time, b.DateSource, want)
		}
	}
	if b1.ContentHash != b2.ContentHash {
		t.Fatal("the edited copies are no longer the same content: the twin shows as a second card")
	}
	if bo := e.mustAsset(other); !bo.TakenLocal.Time.Equal(ao.TakenLocal.Time) {
		t.Fatal("an unrelated photo was edited")
	}
	// Both named: each is reported once, the content edited once.
	res, err = e.editor().Apply(ctx, e.userID, []string{db.UUIDString(a1.ID), db.UUIDString(a2.ID)}, Edit{ShiftMin: 60})
	if err != nil || len(res.Updated) != 2 {
		t.Fatalf("both named: %+v %v", res, err)
	}
	if b := e.mustAsset(n2); !b.TakenLocal.Time.Equal(want.Add(time.Hour)) {
		t.Fatalf("both named: shifted to %v, want %v", b.TakenLocal.Time, want.Add(time.Hour))
	}
}
