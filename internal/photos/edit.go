package photos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/photos/media"
	"discodrive/internal/storage"
)

// Edit is a change of the capture moment and/or the place of a card.
type Edit struct {
	// SetTime is the new capture moment. With TZOffsetMin it is an instant; without, its
	// clock reading is the wall-clock time and no UTC offset is stored.
	SetTime     *time.Time
	TZOffsetMin *int
	// ShiftMin moves every card by the same number of minutes from its own date; it
	// excludes SetTime.
	ShiftMin int
	// SetPlace turns the place edit on: Lat and Lon set the coordinates, nil erases them.
	SetPlace bool
	Lat, Lon *float64
}

// Failure is a card that could not be edited.
type Failure struct {
	ID    string
	Error string
}

// Result reports what Apply did, by requested card id. The batch never stops at one
// card: each id lands in exactly one list.
type Result struct {
	Updated []string
	// Conflicts: a file changed while it was being edited (or since it was indexed). The
	// newer content was kept and nothing of the card was written.
	Conflicts []string
	Failed    []Failure
	// Partial: some files of a multi-file card were written before another conflicted or
	// failed. Retrying a shift would move the written part twice.
	Partial []string
	// VersionBytes is the size of the files that were replaced by a new version: the
	// previous content stays in the version history and takes space.
	VersionBytes int64
}

// ErrBadEdit is returned for an Edit that asks for nothing or for contradictory things.
var ErrBadEdit = errors.New("photos: invalid edit")

// beforeEditCommit runs between preparing the edited copy and publishing it. Tests use
// it to land a concurrent write; a no-op otherwise.
var beforeEditCommit = func() {}

// Editor writes capture date and place into the files of cards: EXIF for the formats
// exiftool writes reliably, an XMP sidecar next to everything else. The original is
// never edited in place: the change is made on a private copy and published through
// PushByPath with the version the copy was made from, so a concurrent change wins and
// the replaced content stays as a file version.
type Editor struct {
	q           *db.Queries
	files       *storage.FileService
	storageRoot string
	tools       media.Tools
	ix          *Indexer
}

// NewEditor creates an Editor; ix re-reads the edited files into the gallery at once.
func NewEditor(q *db.Queries, files *storage.FileService, storageRoot string, tools media.Tools, ix *Indexer) *Editor {
	return &Editor{q: q, files: files, storageRoot: storageRoot, tools: tools, ix: ix}
}

// exifWritable are the formats whose EXIF block exiftool rewrites safely; everything
// else (video, RAW, GIF) gets a sidecar and stays byte for byte as it is.
var exifWritable = map[string]bool{".jpg": true, ".jpeg": true, ".heic": true, ".heif": true, ".png": true, ".webp": true}

// change is what one file gets: the wall clock and offset to write, and the place.
type change struct {
	setDate  bool
	wall     time.Time // wall-clock reading, labelled UTC
	offset   *int
	setPlace bool
	lat, lon *float64
}

// plan derives the values for one asset.
func (e Edit) plan(a db.PhotoAsset) change {
	c := change{setPlace: e.SetPlace, lat: e.Lat, lon: e.Lon}
	switch {
	case e.SetTime != nil:
		c.setDate = true
		if e.TZOffsetMin != nil {
			c.offset = e.TZOffsetMin
			c.wall = e.SetTime.UTC().Add(time.Duration(*e.TZOffsetMin) * time.Minute)
		} else {
			t := *e.SetTime
			c.wall = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
		}
	case e.ShiftMin != 0:
		c.setDate = true
		c.wall = a.TakenLocal.Time.Add(time.Duration(e.ShiftMin) * time.Minute)
		if a.TzOffsetMin.Valid {
			o := int(a.TzOffsetMin.Int32)
			c.offset = &o
		}
	}
	if c.wall.IsZero() && c.setDate {
		c.setDate = false
	}
	return c
}

func (e Edit) validate() error {
	if e.SetTime != nil && e.ShiftMin != 0 {
		return fmt.Errorf("%w: a new time and a shift exclude each other", ErrBadEdit)
	}
	if e.SetTime == nil && e.ShiftMin == 0 && !e.SetPlace {
		return fmt.Errorf("%w: nothing to change", ErrBadEdit)
	}
	if e.SetTime == nil && e.TZOffsetMin != nil {
		return fmt.Errorf("%w: an offset needs a time", ErrBadEdit)
	}
	if e.SetPlace && (e.Lat == nil) != (e.Lon == nil) {
		return fmt.Errorf("%w: latitude and longitude go together", ErrBadEdit)
	}
	if e.Lat != nil && (math.Abs(*e.Lat) > 90 || math.Abs(*e.Lon) > 180 || math.IsNaN(*e.Lat) || math.IsNaN(*e.Lon)) {
		return fmt.Errorf("%w: coordinates out of range", ErrBadEdit)
	}
	return nil
}

// target is one file to publish: the edited copy of an existing node, or a new sidecar.
type target struct {
	node  db.Node // for a new sidecar: the would-be node (name, path, user), Version 0
	tmp   string
	fresh bool
}

// prepared is one asset ready to be published.
type prepared struct {
	asset   db.PhotoAsset
	node    db.Node
	c       change
	targets []target
	pushed  int
	// sidecarOnly: the position can only be written into a sidecar, which cannot erase
	// what the file itself holds.
	sidecarOnly bool
}

// Apply edits the cards named by assetIDs (the caller has checked they are the user's):
// each card's live video and original follow it. All edited copies of a card are made
// before the first is published; a card whose file changed meanwhile is reported in
// Conflicts and left as it is. A failure of one card does not stop the others.
func (e *Editor) Apply(ctx context.Context, userID string, assetIDs []string, ed Edit) (Result, error) {
	var res Result
	if err := ed.validate(); err != nil {
		return res, err
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return res, err
	}
	ids := make([]pgtype.UUID, 0, len(assetIDs))
	for _, s := range assetIDs {
		id, err := db.ParseUUID(s)
		if err != nil {
			return res, err
		}
		ids = append(ids, id)
	}
	roots, err := e.q.ListOwnPhotoAssets(ctx, db.ListOwnPhotoAssetsParams{UserID: uid, Ids: ids})
	if err != nil {
		return res, err
	}
	kids, err := e.q.ListPhotoChildren(ctx, ids)
	if err != nil {
		return res, err
	}
	kidIDs := make([]pgtype.UUID, len(kids))
	for i, k := range kids {
		kidIDs[i] = k.ID
	}
	kidRows, err := e.q.ListOwnPhotoAssets(ctx, db.ListOwnPhotoAssetsParams{UserID: uid, Ids: kidIDs})
	if err != nil {
		return res, err
	}
	kidByID := map[pgtype.UUID]db.PhotoAsset{}
	for _, k := range kidRows {
		kidByID[k.ID] = k
	}
	members := map[pgtype.UUID][]db.PhotoAsset{}
	for _, k := range kids {
		if a, ok := kidByID[k.ID]; ok {
			members[k.ParentID] = append(members[k.ParentID], a)
		}
	}
	rootByID := map[pgtype.UUID]db.PhotoAsset{}
	for _, r := range roots {
		rootByID[r.ID] = r
	}
	// Copies of a card (same content elsewhere in the sources) are edited with it, as
	// flags and delete treat them: an untouched twin would otherwise stop being a duplicate
	// once the edited file changes, and come back as a card with the old values.
	copies, err := e.q.ListPhotoCardCopies(ctx, db.ListPhotoCardCopiesParams{UserID: uid, Ids: ids})
	if err != nil {
		return res, err
	}
	copyIDs := make([]pgtype.UUID, 0, len(copies))
	for _, c := range copies {
		copyIDs = append(copyIDs, c.CopyID)
	}
	copyRows, err := e.q.ListOwnPhotoAssets(ctx, db.ListOwnPhotoAssetsParams{UserID: uid, Ids: copyIDs})
	if err != nil {
		return res, err
	}
	copyKids, err := e.q.ListPhotoChildren(ctx, copyIDs)
	if err != nil {
		return res, err
	}
	copyKidIDs := make([]pgtype.UUID, len(copyKids))
	for i, k := range copyKids {
		copyKidIDs[i] = k.ID
	}
	copyKidRows, err := e.q.ListOwnPhotoAssets(ctx, db.ListOwnPhotoAssetsParams{UserID: uid, Ids: copyKidIDs})
	if err != nil {
		return res, err
	}
	for _, k := range copyKidRows {
		kidByID[k.ID] = k
	}
	for _, k := range copyKids {
		if a, ok := kidByID[k.ID]; ok {
			members[k.ParentID] = append(members[k.ParentID], a)
		}
	}
	copyByID := map[pgtype.UUID]db.PhotoAsset{}
	for _, c := range copyRows {
		copyByID[c.ID] = c
	}
	requested := make(map[pgtype.UUID]bool, len(ids))
	for _, id := range ids {
		requested[id] = true
	}
	for _, c := range copies {
		if requested[c.CopyID] {
			continue // named itself: edited (and reported) as its own card
		}
		if row, ok := copyByID[c.CopyID]; ok {
			// The copy and its own children follow the card.
			members[c.CardID] = append(members[c.CardID], append([]db.PhotoAsset{row}, members[c.CopyID]...)...)
		}
	}
	seen := map[pgtype.UUID]bool{} // an asset named twice (a card and its child) is edited once
	for _, id := range ids {
		r, ok := rootByID[id]
		if !ok || seen[id] {
			continue
		}
		sid := db.UUIDString(id)
		if ctx.Err() != nil {
			res.Failed = append(res.Failed, Failure{sid, "canceled"})
			continue
		}
		var cardAssets []db.PhotoAsset
		for _, a := range append([]db.PhotoAsset{r}, members[id]...) {
			if !seen[a.ID] {
				seen[a.ID] = true
				cardAssets = append(cardAssets, a)
			}
		}
		e.applyCard(ctx, userID, sid, cardAssets, ed, &res)
	}
	return res, nil
}

// applyCard edits one card and files it under exactly one list of res.
func (e *Editor) applyCard(ctx context.Context, userID, cardID string, assets []db.PhotoAsset, ed Edit, res *Result) {
	dir, err := os.MkdirTemp("", "photo-edit-")
	if err != nil {
		res.Failed = append(res.Failed, Failure{cardID, err.Error()})
		return
	}
	defer os.RemoveAll(dir)

	var preps []*prepared
	for i, a := range assets {
		p, conflict, err := e.prepare(ctx, filepath.Join(dir, strconv.Itoa(i)), a, ed)
		if err != nil {
			log.Printf("discodrive: photos edit %s: %v", cardID, err)
			res.Failed = append(res.Failed, Failure{cardID, err.Error()})
			return
		}
		if conflict {
			res.Conflicts = append(res.Conflicts, cardID)
			return
		}
		if p != nil {
			preps = append(preps, p)
		}
	}

	var pushErr error
	conflict := false
	pushedAny := false
loop:
	for _, p := range preps {
		for _, t := range p.targets {
			c, err := e.publish(ctx, userID, t, res)
			if err != nil {
				pushErr = err
				break loop
			}
			if c {
				conflict = true
				break loop
			}
			p.pushed++
			pushedAny = true
		}
	}
	// What was written is read back, even when the card did not finish: the rows follow
	// the files.
	for _, p := range preps {
		if pushErr == nil && !conflict || p.pushed > 0 {
			if err := e.finish(ctx, p); err != nil && pushErr == nil {
				pushErr = err
			}
		}
	}
	switch {
	case pushErr != nil && pushedAny:
		log.Printf("discodrive: photos edit %s: %v", cardID, pushErr)
		res.Partial = append(res.Partial, cardID)
	case pushErr != nil:
		log.Printf("discodrive: photos edit %s: %v", cardID, pushErr)
		res.Failed = append(res.Failed, Failure{cardID, pushErr.Error()})
	case conflict && pushedAny:
		res.Partial = append(res.Partial, cardID)
	case conflict:
		res.Conflicts = append(res.Conflicts, cardID)
	default:
		res.Updated = append(res.Updated, cardID)
	}
}

// finish brings the asset's row up to date after its files were written.
func (e *Editor) finish(ctx context.Context, p *prepared) error {
	if p.c.setPlace {
		cleared := p.c.lat == nil && p.sidecarOnly
		if err := e.q.SetPhotoPlaceCleared(ctx, db.SetPhotoPlaceClearedParams{NodeID: p.node.ID, Cleared: cleared}); err != nil {
			return err
		}
	}
	return e.ix.ApplyEdited(ctx, db.UUIDString(p.node.ID), p.c.setDate)
}

// prepare makes the edited copies of one asset's files in dir: the media file itself when
// it takes EXIF, a sidecar otherwise, and an existing sidecar always (it overrides the
// file when read). A nil result means the asset is gone. conflict: the file is newer than
// the row the edit was computed from.
func (e *Editor) prepare(ctx context.Context, dir string, a db.PhotoAsset, ed Edit) (*prepared, bool, error) {
	node, err := e.q.GetNode(ctx, a.NodeID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (node.IsDir || !node.DiskPath.Valid)) {
		return nil, false, nil // gone meanwhile; nothing to edit
	} else if err != nil {
		return nil, false, err
	}
	if a.NodeVersion.Valid && a.NodeVersion.Int64 != node.Version {
		return nil, true, nil // not indexed yet: a shift would start from a stale date
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	p := &prepared{asset: a, node: node, c: ed.plan(a)}
	side, err := e.findSidecar(ctx, node)
	if err != nil {
		return nil, false, err
	}
	wroteFile := false
	if exifWritable[strings.ToLower(filepath.Ext(node.Name))] {
		// A name made here cannot start with "-" or hold a line break, whatever the original has.
		tmp := filepath.Join(dir, "f"+safeExt(node.Name))
		if err := e.copyOut(node, tmp); err != nil {
			return nil, false, err
		}
		changed, err := e.exiftool(ctx, append(exifArgs(p.c), tmp))
		if err == nil {
			err = e.readsBack(ctx, tmp, p.c)
		}
		switch {
		case err == nil:
			wroteFile = true
			if changed {
				p.targets = append(p.targets, target{node: node, tmp: tmp})
			}
		case errors.Is(err, errExiftool):
			log.Printf("discodrive: photos edit %s: %v (a sidecar is written instead)", node.DiskPath.String, err)
		default:
			return nil, false, err
		}
	}
	if !wroteFile || side != nil {
		p.sidecarOnly = !wroteFile
		t, ok, err := e.prepareSidecar(ctx, dir, node, side, p.c)
		if err != nil {
			return nil, false, err
		}
		if ok {
			p.targets = append(p.targets, t)
		}
	}
	return p, false, nil
}

// prepareSidecar edits <name>.xmp (or the existing <stem>.xmp) on a copy; a new one is
// made from an empty XMP packet. ok=false: nothing to publish (no change).
func (e *Editor) prepareSidecar(ctx context.Context, dir string, node db.Node, side *db.Node, c change) (target, bool, error) {
	tmp := filepath.Join(dir, "f.xmp")
	t := target{tmp: tmp}
	if side != nil {
		t.node = *side
		if err := e.copyOut(*side, tmp); err != nil {
			return t, false, err
		}
	} else {
		t.node = node
		t.node.Name = node.Name + ".xmp"
		t.node.DiskPath = pgtype.Text{String: node.DiskPath.String + ".xmp", Valid: true}
		t.node.Version = 0
		t.fresh = true
		if err := os.WriteFile(tmp, []byte(emptyXMP), 0o600); err != nil {
			return t, false, err
		}
	}
	changed, err := e.exiftool(ctx, append(xmpArgs(c), tmp))
	if err != nil {
		return t, false, err
	}
	return t, changed, nil
}

// findSidecar returns the sidecar that describes node: <name>.xmp before <stem>.xmp.
func (e *Editor) findSidecar(ctx context.Context, node db.Node) (*db.Node, error) {
	p := node.DiskPath.String
	stem := strings.TrimSuffix(p, filepath.Ext(p))
	for _, cand := range []string{p + ".xmp", stem + ".xmp"} {
		n, err := e.q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: node.UserID, Path: cand})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		} else if err != nil {
			return nil, err
		}
		if !n.IsDir {
			return &n, nil
		}
	}
	return nil, nil
}

// readsBack re-reads the written copy the way the indexer does. exiftool can write tags it
// then does not read: EXIF in a PNG behind a C2PA manifest (ChatGPT images) is taken for
// a loop and skipped. Such a write is an errExiftool, so the sidecar takes the edit.
func (e *Editor) readsBack(ctx context.Context, path string, c change) error {
	if e.ix == nil || e.ix.exif == nil {
		return nil
	}
	m, err := e.ix.exif.Read(ctx, path)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%w: re-read: %v", errExiftool, err)
	}
	if !holds(m, c) {
		return fmt.Errorf("%w: the written values do not read back", errExiftool)
	}
	return nil
}

// holds reports whether metadata read from a file carries the change.
func holds(m media.Meta, c change) bool {
	if c.setDate {
		if m.TakenAt == nil {
			return false
		}
		wall := *m.TakenAt
		if m.TZOffsetMin != nil {
			wall = wall.Add(time.Duration(*m.TZOffsetMin) * time.Minute)
		}
		if exifDate(wall) != exifDate(c.wall) {
			return false
		}
		if c.offset != nil && (m.TZOffsetMin == nil || *m.TZOffsetMin != *c.offset) {
			return false
		}
	}
	if c.setPlace {
		if c.lat == nil || c.lon == nil {
			return m.Lat == nil && m.Lon == nil
		}
		near := func(a *float64, b float64) bool { return a != nil && math.Abs(*a-b) < 1e-5 }
		return near(m.Lat, *c.lat) && near(m.Lon, *c.lon)
	}
	return true
}

// errExiftool marks a failure of the tool itself (the format cannot be written, the
// copy is damaged): the file stays untouched and a sidecar takes the edit instead.
var errExiftool = errors.New("exiftool failed")

// copyOut copies the live content of a node into dst, through the checked opener.
func (e *Editor) copyOut(node db.Node, dst string) error {
	src, err := storage.NewLocalDisk(e.storageRoot).OpenAbsolute(filepath.Join(e.storageRoot, node.DiskPath.String))
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, src)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// publish stores an edited copy as the next version of its node (or creates a new
// sidecar). A stale version keeps the newer content, drops the copy and reports a
// conflict.
func (e *Editor) publish(ctx context.Context, userID string, t target, res *Result) (bool, error) {
	beforeEditCommit()
	f, err := os.Open(t.tmp)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var base *int64
	// An edit of the metadata is not new content as far as the file's date goes: the node
	// keeps its modification time, which is the capture date of a file without one inside
	// (a scan, a messenger JPEG) and would otherwise become "now".
	meta := storage.PushMeta{RejectStale: true}
	if t.fresh {
		// Somebody made the sidecar meanwhile: theirs wins.
		if _, err := e.q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: t.node.UserID, Path: t.node.DiskPath.String}); err == nil {
			return true, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	} else {
		v := t.node.Version
		base = &v
		if t.node.ModifiedAt.Valid {
			meta.ModifiedAt = t.node.ModifiedAt.Time
		}
	}
	rel := strings.TrimPrefix(t.node.DiskPath.String, userID+"/")
	pr, err := e.files.PushByPathWithMeta(ctx, userID, rel, base, f, meta)
	if errors.Is(err, storage.ErrStaleVersion) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if pr.Conflicted {
		return true, nil
	}
	if base != nil {
		res.VersionBytes += t.node.Size.Int64
	}
	return false, nil
}

// safeExt is the file's extension when it is plain, "" otherwise.
func safeExt(name string) string {
	ext := filepath.Ext(name)
	for _, r := range ext[min(1, len(ext)):] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return ext
}

// exiftool runs one write command. changed=false: the tags already held the values (or,
// for deletions, were absent), so the file is as it should be and needs no new version.
func (e *Editor) exiftool(ctx context.Context, args []string) (changed bool, err error) {
	if e.tools.Exiftool == "" {
		return false, errors.New("exiftool is not available")
	}
	all := append([]string{"-overwrite_original", "-P", "-api", "largefilesupport=1"}, args...)
	out, err := exec.CommandContext(ctx, e.tools.Exiftool, all...).CombinedOutput()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err == nil && bytes.Contains(out, []byte("1 image files updated")) {
		return true, nil
	}
	if err == nil && bytes.Contains(out, []byte("1 image files unchanged")) {
		return false, nil
	}
	msg := strings.TrimSpace(string(out))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return false, fmt.Errorf("%w: %v: %s", errExiftool, err, msg)
}

const emptyXMP = `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about=""/></rdf:RDF></x:xmpmeta>
<?xpacket end="w"?>
`

func exifDate(t time.Time) string { return t.Format("2006:01:02 15:04:05") }

func exifOffset(min int) string {
	sign := "+"
	if min < 0 {
		sign, min = "-", -min
	}
	return fmt.Sprintf("%s%02d:%02d", sign, min/60, min%60)
}

func coord(v float64) string { return strconv.FormatFloat(v, 'f', 7, 64) }

// exifArgs are the exiftool arguments for a file with an EXIF block. Stale copies of the
// same values in XMP are erased so no reader prefers the old ones.
func exifArgs(c change) []string {
	var a []string
	if c.setDate {
		d := exifDate(c.wall)
		a = append(a, "-EXIF:DateTimeOriginal="+d, "-EXIF:CreateDate="+d, "-EXIF:SubSecTimeOriginal=",
			"-XMP-exif:DateTimeOriginal=", "-XMP-xmp:CreateDate=", "-XMP-photoshop:DateCreated=")
		o := ""
		if c.offset != nil {
			o = exifOffset(*c.offset)
		}
		// All three: the reader falls back from one to the next, so a stale one would win.
		a = append(a, "-EXIF:OffsetTimeOriginal="+o, "-EXIF:OffsetTimeDigitized="+o, "-EXIF:OffsetTime="+o)
	}
	if c.setPlace {
		a = append(a, "-EXIF:GPSAltitude=", "-EXIF:GPSAltitudeRef=",
			"-XMP-exif:GPSLatitude=", "-XMP-exif:GPSLongitude=", "-XMP-exif:GPSAltitude=")
		if c.lat == nil {
			a = append(a, "-EXIF:GPSLatitude=", "-EXIF:GPSLatitudeRef=", "-EXIF:GPSLongitude=", "-EXIF:GPSLongitudeRef=")
		} else {
			latRef, lonRef := "N", "E"
			if *c.lat < 0 {
				latRef = "S"
			}
			if *c.lon < 0 {
				lonRef = "W"
			}
			a = append(a, "-EXIF:GPSLatitude="+coord(math.Abs(*c.lat)), "-EXIF:GPSLatitudeRef="+latRef,
				"-EXIF:GPSLongitude="+coord(math.Abs(*c.lon)), "-EXIF:GPSLongitudeRef="+lonRef)
		}
	}
	return a
}

// xmpArgs are the arguments for an XMP sidecar: the reader takes the date and the
// position from XMP-exif.
func xmpArgs(c change) []string {
	var a []string
	if c.setDate {
		d := exifDate(c.wall)
		if c.offset != nil {
			d += exifOffset(*c.offset)
		}
		a = append(a, "-XMP-exif:DateTimeOriginal="+d, "-XMP-xmp:CreateDate="+d)
	}
	if c.setPlace {
		if c.lat == nil {
			a = append(a, "-XMP-exif:GPSLatitude=", "-XMP-exif:GPSLongitude=", "-XMP-exif:GPSAltitude=")
		} else {
			a = append(a, "-XMP-exif:GPSLatitude="+coord(*c.lat), "-XMP-exif:GPSLongitude="+coord(*c.lon),
				"-XMP-exif:GPSAltitude=")
		}
	}
	return a
}
