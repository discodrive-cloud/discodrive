package photos

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/coalesce"
	"discodrive/internal/db"
	"discodrive/internal/photos/media"
	"discodrive/internal/storage"
)

// Geocoder turns coordinates into a place (its GeoNames id) and an ISO country code, and
// names a place in a language. ok=false means the lookup found nothing (the asset then
// stores 0 so it is not looked up again).
type Geocoder interface {
	Lookup(lat, lon float64) (placeID int32, country string, ok bool)
	Name(placeID int32, lang string) string
}

// Indexer keeps photo_assets in step with the user's source folders. It implements
// library.Library (driven by the change log) and library.AfterCatchUp: Live Photo and
// edited pairs are regrouped over the whole library once a pass that changed anything
// ends, and derivative files of purged assets are swept once a purge happened.
type Indexer struct {
	q           *db.Queries
	storageRoot string
	exif        *media.Exif
	geo         Geocoder

	mu     sync.Mutex
	dirty  map[pgtype.UUID]bool  // users whose assets changed since the last regroup
	swept  map[pgtype.UUID]int64 // newest purge seq whose orphans were swept, per user
	users  map[pgtype.UUID]*sync.Mutex
	passes coalesce.Gate
	// progress of the full passes, per user: files listed as stale and not read yet,
	// and whether a pass is queued or running.
	pending  map[pgtype.UUID]int64
	scanning map[pgtype.UUID]int
	bg       sync.WaitGroup // full passes started by Heal

	// batch is how many files a full pass reads under the user's lock before it lets a
	// catch-up's regroup in (tests make it small).
	batch int
	// afterBatch, when set, runs after each batch with the lock released (tests).
	afterBatch func()

	reads atomic.Int64 // metadata reads (exiftool), for tests

	// OnPending, when set (before the indexer runs), is called after an asset was
	// stored with a thumbnail still to be made; it must not block (Thumbs.Kick).
	OnPending func()
}

// userLock serialises a user's regroups, sweeps and the batches of a full pass: the
// tracker's catch-up and an API Reindex run under different gates, and two regroups from
// different snapshots could otherwise write a stale link last. A full pass takes it per
// batch, never for the whole pass, so a catch-up waits at most one batch.
func (ix *Indexer) userLock(userID pgtype.UUID) *sync.Mutex {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	m := ix.users[userID]
	if m == nil {
		m = &sync.Mutex{}
		ix.users[userID] = m
	}
	return m
}

// geoReady reports whether lookups mean anything yet: a hot-swappable geocoder that has
// no data loaded is treated like no geocoder, so assets stay NULL for the backfill.
func (ix *Indexer) geoReady() bool {
	if ix.geo == nil {
		return false
	}
	if r, ok := ix.geo.(interface{ Ready() bool }); ok {
		return r.Ready()
	}
	return true
}

// PlaceName names a stored place in lang ("" when there is none or no geocoder).
func (ix *Indexer) PlaceName(id pgtype.Int4, lang string) string {
	if ix == nil || ix.geo == nil || !id.Valid || id.Int32 == 0 {
		return ""
	}
	return ix.geo.Name(id.Int32, lang)
}

// NewIndexer creates the photos library. geo may be nil: place and country then stay
// NULL until a geocoder fills them.
func NewIndexer(q *db.Queries, storageRoot string, exif *media.Exif, geo Geocoder) *Indexer {
	return &Indexer{q: q, storageRoot: storageRoot, exif: exif, geo: geo,
		dirty: map[pgtype.UUID]bool{}, swept: map[pgtype.UUID]int64{}, users: map[pgtype.UUID]*sync.Mutex{},
		pending: map[pgtype.UUID]int64{}, scanning: map[pgtype.UUID]int{}, batch: passBatch}
}

// passBatch is the number of files a full pass reads per hold of the user's lock
// (~200 exiftool reads: seconds to a minute).
const passBatch = 200

// Progress reports the files a full pass has found and not read yet, and whether a full
// pass is queued or running for the user.
func (ix *Indexer) Progress(userID pgtype.UUID) (pending int64, scanning bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.pending[userID], ix.scanning[userID] > 0
}

func (ix *Indexer) addPending(userID pgtype.UUID, n int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.pending[userID] += n; ix.pending[userID] <= 0 {
		delete(ix.pending, userID)
	}
}

func (ix *Indexer) setScanning(userID pgtype.UUID, on bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if on {
		ix.scanning[userID]++
	} else if ix.scanning[userID]--; ix.scanning[userID] <= 0 {
		delete(ix.scanning, userID)
	}
}

// Wait blocks until the full passes started by Heal have ended (tests, shutdown).
func (ix *Indexer) Wait() { ix.bg.Wait() }

// mediaExts is what the gallery shows; ListStalePhotoNodes matches the same list.
var mediaExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".heic": true, ".heif": true, ".png": true, ".webp": true,
	".gif": true, ".dng": true, ".nef": true, ".cr2": true, ".arw": true,
	".mov": true, ".mp4": true, ".m4v": true, ".3gp": true, ".avi": true,
}

// IsMediaFile reports whether the gallery shows the file.
func IsMediaFile(name string) bool { return mediaExts[strings.ToLower(filepath.Ext(name))] }

func isSidecar(name string) bool { return strings.EqualFold(filepath.Ext(name), ".xmp") }

func (ix *Indexer) Name() string { return "photos" }

// State returns the disk paths of the user's sources. Trashed or not, as for the music
// folder: a source's own "delete" in the log must still match its path.
func (ix *Indexer) State(ctx context.Context, userID pgtype.UUID) ([]string, int64, bool, error) {
	ps, err := ix.q.GetPhotoSettings(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, false, nil
	} else if err != nil {
		return nil, 0, false, err
	}
	if !ps.Enabled {
		return nil, 0, false, nil
	}
	roots, err := ix.roots(ctx, userID)
	if err != nil || len(roots) == 0 {
		return nil, 0, false, err
	}
	return rootPaths(roots), ps.IndexedSeq, true, nil
}

// roots returns the folder nodes of the user's sources in any state; a purged source is
// gone (its assets went with the cascade).
func (ix *Indexer) roots(ctx context.Context, userID pgtype.UUID) ([]db.Node, error) {
	sources, err := ix.q.ListPhotoSources(ctx, userID)
	if err != nil {
		return nil, err
	}
	var roots []db.Node
	for _, s := range sources {
		n, err := ix.q.GetNodeAnyState(ctx, s.FolderNodeID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		} else if err != nil {
			return nil, err
		}
		if n.DiskPath.Valid {
			roots = append(roots, n)
		}
	}
	return roots, nil
}

func rootPaths(roots []db.Node) []string {
	paths := make([]string, len(roots))
	for i, r := range roots {
		paths[i] = r.DiskPath.String
	}
	return paths
}

func (ix *Indexer) SetCursor(ctx context.Context, userID pgtype.UUID, seq int64) error {
	return ix.q.SetPhotoIndexedSeq(ctx, db.SetPhotoIndexedSeqParams{UserID: userID, IndexedSeq: seq})
}

// Accepts media files and XMP sidecars: a sidecar is no card of its own, but its change
// re-reads the file it describes.
func (ix *Indexer) Accepts(diskPath string) bool {
	return IsMediaFile(diskPath) || isSidecar(diskPath)
}

func (ix *Indexer) Index(ctx context.Context, userID, nodeID, diskPath string) error {
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return err
	}
	if isSidecar(diskPath) {
		return ix.reindexSidecarTargets(ctx, nid)
	}
	return ix.indexNode(ctx, nid, false)
}

// indexNode reads the live node's metadata into its asset. An asset already at the
// node's version is left alone; one whose content is unchanged (a rename, a trash and
// restore: each bumps the version) only records the new version, keeping its thumbnail.
// force re-reads anyway (its sidecar changed).
func (ix *Indexer) indexNode(ctx context.Context, nid pgtype.UUID, force bool) error {
	node, err := ix.q.GetNode(ctx, nid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // gone meanwhile; its own change comes next
	} else if err != nil {
		return err
	}
	if node.IsDir || !node.DiskPath.Valid || node.IsVault || node.IsConflictLoser || !IsMediaFile(node.Name) {
		return ix.removeAsset(ctx, nid) // encrypted, a conflict copy, or no media (any more)
	}
	a, err := ix.q.GetPhotoAssetByNode(ctx, nid)
	have := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if have && !force {
		sameVersion := a.NodeVersion.Valid && a.NodeVersion.Int64 == node.Version
		sameContent := a.ContentHash.Valid && node.ContentHash.Valid && a.ContentHash.String == node.ContentHash.String
		if sameVersion || sameContent {
			if !sameVersion {
				if err := ix.q.SetPhotoNodeVersion(ctx, db.SetPhotoNodeVersionParams{NodeID: nid,
					NodeVersion: pgtype.Int8{Int64: node.Version, Valid: true}}); err != nil {
					return err
				}
				ix.markDirty(node.UserID) // the name may have changed: it pairs files
			}
			if a.TrashedAt.Valid {
				ix.markDirty(node.UserID)
				return ix.q.UntrashPhotoAssetByNode(ctx, nid)
			}
			return nil
		}
	}

	abs := filepath.Join(ix.storageRoot, node.DiskPath.String)
	// exiftool opens the file by name; refuse a path that leaves the storage root
	// through a symlink first (the same check every other reader of user files makes).
	f, err := storage.NewLocalDisk(ix.storageRoot).OpenAbsolute(abs)
	if err != nil {
		return err
	}
	_ = f.Close()
	ix.reads.Add(1)
	meta, rerr := ix.exif.Read(ctx, abs)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if rerr != nil {
		// Still indexed with what could be read (at worst the file date): a file of the
		// sources must show up even when its metadata is broken.
		log.Printf("discodrive: photos %s: %v", node.DiskPath.String, rerr)
	}
	p := ix.assetParams(node, meta)
	if rerr != nil {
		p.IndexError = pgtype.Text{String: truncate(rerr.Error(), maxErrLen), Valid: true}
	}
	saved, err := ix.q.UpsertPhotoAsset(ctx, p)
	if err != nil {
		return err
	}
	if have && a.NodeVersion != saved.NodeVersion {
		// New content: the viewer preview and the h264 copy of the old one are named by
		// the old content and are never served again; drop them now rather than at the
		// asset's end. (The thumbnail went back to pending and is re-rendered in place.)
		dropStaleDerivatives(ix.storageRoot, saved)
	}
	if saved.ThumbState == "pending" && ix.OnPending != nil {
		ix.OnPending()
	}
	if have && a.TrashedAt.Valid {
		if err := ix.q.UntrashPhotoAssetByNode(ctx, nid); err != nil {
			return err
		}
	}
	ix.markDirty(node.UserID)
	return nil
}

// ApplyEdited re-reads a node whose date or place the Editor just wrote (into the file or
// into its sidecar), so the gallery shows the edit before the change log is processed.
// dateEdited marks the capture date as set by the user. The re-read already goes through
// the geocoder, and the tracker's later pass finds the node at its version.
func (ix *Indexer) ApplyEdited(ctx context.Context, nodeID string, dateEdited bool) error {
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return err
	}
	if err := ix.indexNode(ctx, nid, true); err != nil {
		return err
	}
	if dateEdited {
		return ix.q.SetPhotoDateEdited(ctx, nid)
	}
	return nil
}

func (ix *Indexer) assetParams(node db.Node, m media.Meta) db.UpsertPhotoAssetParams {
	p := db.UpsertPhotoAssetParams{
		UserID:      node.UserID,
		NodeID:      node.ID,
		Kind:        m.Kind,
		Make:        text(m.Make),
		Model:       text(m.Model),
		Lens:        text(m.Lens),
		Exposure:    text(m.ExposureS),
		VideoCodec:  text(m.VideoCodec),
		LiveCid:     text(m.LiveCID),
		ContentHash: node.ContentHash,
		IsHdr:       m.HDR,
		NodeVersion: pgtype.Int8{Int64: node.Version, Valid: true},
	}
	if p.Kind == "" {
		p.Kind = "photo"
	}
	takenAt, local, src := takenTimes(m, node.ModifiedAt.Time)
	p.TakenAt = pgtype.Timestamptz{Time: takenAt, Valid: true}
	p.TakenLocal = pgtype.Timestamp{Time: local, Valid: true}
	p.DateSource = src
	if m.TakenAt != nil && m.TZOffsetMin != nil {
		p.TzOffsetMin = pgtype.Int4{Int32: int32(*m.TZOffsetMin), Valid: true}
	}
	if m.Lat != nil && m.Lon != nil {
		p.Lat = pgtype.Float8{Float64: *m.Lat, Valid: true}
		p.Lon = pgtype.Float8{Float64: *m.Lon, Valid: true}
		if ix.geoReady() {
			// A failed lookup stores 0 so ListPhotoAssetsWithoutPlace does not pick it
			// again; without a geocoder both stay NULL for one that comes later.
			id, country, ok := ix.geo.Lookup(*m.Lat, *m.Lon)
			if !ok {
				id, country = 0, ""
			}
			p.PlaceID = pgtype.Int4{Int32: id, Valid: true}
			p.CountryCode = pgtype.Text{String: country, Valid: true}
		}
	}
	if m.FNumber > 0 {
		p.FNumber = pgtype.Float4{Float32: float32(m.FNumber), Valid: true}
	}
	if m.FocalMM > 0 {
		p.FocalMm = pgtype.Float4{Float32: float32(m.FocalMM), Valid: true}
	}
	if m.ISO > 0 {
		p.Iso = pgtype.Int4{Int32: int32(m.ISO), Valid: true}
	}
	if m.Width > 0 && m.Height > 0 {
		p.Width = pgtype.Int4{Int32: int32(m.Width), Valid: true}
		p.Height = pgtype.Int4{Int32: int32(m.Height), Valid: true}
	}
	if m.Kind == "video" && m.DurationMS > 0 {
		p.DurationMs = pgtype.Int8{Int64: m.DurationMS, Valid: true}
	}
	return p
}

// takenTimes returns the capture instant, the wall clock at the place of capture and
// where they came from. Without a date in the file the node's modification time is used,
// its wall clock read in the server's zone (the place's zone is unknown).
func takenTimes(m media.Meta, modified time.Time) (at, local time.Time, source string) {
	if m.TakenAt == nil {
		w := modified.In(time.Local)
		return modified.UTC(), wallClock(w), "file"
	}
	at = m.TakenAt.UTC()
	source = "exif"
	if m.DateFromXMP {
		source = "xmp"
	}
	if m.TZOffsetMin != nil {
		return at, wallClock(at.Add(time.Duration(*m.TZOffsetMin) * time.Minute)), source
	}
	// No offset: Meta holds the camera's wall clock labelled UTC.
	return at, wallClock(at), source
}

// wallClock drops the zone, keeping the clock reading (taken_local is a timestamp
// without zone).
func wallClock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// reindexSidecarTargets re-reads the media files an XMP sidecar node describes:
// <name>.<ext>.xmp describes <name>.<ext>, <stem>.xmp every media file named <stem>.*
// in the same folder.
func (ix *Indexer) reindexSidecarTargets(ctx context.Context, sidecar pgtype.UUID) error {
	n, err := ix.q.GetNodeAnyState(ctx, sidecar)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if !n.ParentID.Valid {
		return nil
	}
	siblings, err := ix.q.ListChildren(ctx, db.ListChildrenParams{UserID: n.UserID, ParentID: n.ParentID})
	if err != nil {
		return err
	}
	base := n.Name[:len(n.Name)-len(filepath.Ext(n.Name))]
	var errs []error
	for _, s := range siblings {
		if s.IsDir || !IsMediaFile(s.Name) {
			continue
		}
		if s.Name != base && strings.TrimSuffix(s.Name, filepath.Ext(s.Name)) != base {
			continue
		}
		if _, err := ix.q.GetPhotoAssetByNode(ctx, s.ID); err != nil {
			continue // not in the gallery (outside the sources); nothing to refresh
		}
		if err := ix.indexNode(ctx, s.ID, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Remove follows a node that left the gallery: trashed (the asset goes to the gallery
// trash), moved out of the sources or no longer a media file (the asset and its
// derivatives go). A sidecar's departure re-reads the file it described.
func (ix *Indexer) Remove(ctx context.Context, nodeID string) error {
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return err
	}
	n, err := ix.q.GetNodeAnyState(ctx, nid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && isSidecar(n.Name) {
		return ix.reindexSidecarTargets(ctx, nid)
	}
	if err == nil && n.DeletedAt.Valid {
		ix.markDirty(n.UserID)
		return ix.q.TrashPhotoAssetByNode(ctx, nid)
	}
	return ix.removeAsset(ctx, nid)
}

func (ix *Indexer) removeAsset(ctx context.Context, nid pgtype.UUID) error {
	a, err := ix.q.GetPhotoAssetByNode(ctx, nid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	ids, err := ix.q.DeletePhotoAssetByNode(ctx, nid)
	if err != nil {
		return err
	}
	ix.dropDerivatives(a.UserID, ids)
	ix.markDirty(a.UserID)
	return nil
}

func (ix *Indexer) dropDerivatives(userID pgtype.UUID, ids []pgtype.UUID) {
	uid := db.UUIDString(userID)
	for _, id := range ids {
		removeDerivatives(ix.storageRoot, uid, db.UUIDString(id))
	}
}

// IndexUnder follows a folder restored from the trash or moved into a source: its
// assets leave the gallery trash, its new or changed files are read.
func (ix *Indexer) IndexUnder(ctx context.Context, userID, dirNodeID string) error {
	did, err := db.ParseUUID(dirNodeID)
	if err != nil {
		return err
	}
	dir, err := ix.q.GetNode(ctx, did)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if err := ix.q.UntrashPhotoAssetsUnderPath(ctx, db.UntrashPhotoAssetsUnderPathParams{
		UserID: dir.UserID, Prefix: likePrefix(dir.DiskPath.String)}); err != nil {
		return err
	}
	ix.markDirty(dir.UserID)
	return ix.indexStale(ctx, dir.UserID, did)
}

// indexStale reads every media file of the folder's subtree whose asset is missing or
// older than the file, newest first. One file's failure does not stop the rest. The files
// are read in batches, each under the user's lock and released in between, so a catch-up
// of new uploads (and its regroup) interleaves with a pass over a big archive instead of
// waiting for all of it.
func (ix *Indexer) indexStale(ctx context.Context, userID, folder pgtype.UUID) error {
	nodes, err := ix.q.ListStalePhotoNodes(ctx, folder)
	if err != nil {
		return err
	}
	left := int64(len(nodes))
	ix.addPending(userID, left)
	defer func() { ix.addPending(userID, -left) }()
	batch := max(ix.batch, 1)
	l := ix.userLock(userID)
	for start := 0; start < len(nodes); start += batch {
		end := min(start+batch, len(nodes))
		l.Lock()
		for _, n := range nodes[start:end] {
			if ctx.Err() != nil {
				break
			}
			if IsMediaFile(n.Name) {
				if err := ix.indexNode(ctx, n.ID, false); err != nil && ctx.Err() == nil {
					log.Printf("discodrive: photos %s: %v", n.DiskPath.String, err)
				}
			}
			left--
			ix.addPending(userID, -1)
		}
		l.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ix.afterBatch != nil {
			ix.afterBatch()
		}
	}
	return nil
}

// RemoveUnder follows a folder event the tracker could not place inside a source: the
// folder was trashed, or it lies outside every source path — moved out, or an ANCESTOR of
// a source that was trashed, restored, renamed or moved (the log holds only that folder).
// The assets under it are reconciled by their node's state instead of dropped blindly:
// trashed nodes go to the gallery trash, nodes under no source go (with derivatives),
// nodes still under a source stay (node ids survive renames and moves). A live folder
// that contains sources (a restored or moved-in ancestor) also gets those sources
// untrashed and scanned, so whatever is missing is read.
func (ix *Indexer) RemoveUnder(ctx context.Context, userID pgtype.UUID, dirPath string) error {
	roots, err := ix.roots(ctx, userID)
	if err != nil {
		return err
	}
	prefix := likePrefix(dirPath)
	if err := ix.q.TrashPhotoAssetsUnderPath(ctx, db.TrashPhotoAssetsUnderPathParams{UserID: userID, Prefix: prefix}); err != nil {
		return err
	}
	ids, err := ix.q.DeletePhotoAssetsUnderPathOutsideRoots(ctx, db.DeletePhotoAssetsUnderPathOutsideRootsParams{
		UserID: userID, Prefix: prefix, Roots: rootPaths(roots)})
	ix.dropDerivatives(userID, ids)
	if err != nil {
		return err
	}
	ix.markDirty(userID)
	for _, r := range roots {
		p := r.DiskPath.String
		if r.DeletedAt.Valid || (p != dirPath && !strings.HasPrefix(p, dirPath+"/")) {
			continue
		}
		if err := ix.q.UntrashPhotoAssetsUnderPath(ctx, db.UntrashPhotoAssetsUnderPathParams{
			UserID: userID, Prefix: likePrefix(p)}); err != nil {
			return err
		}
		if err := ix.indexStale(ctx, userID, r.ID); err != nil {
			return err
		}
	}
	return nil
}

// likePrefix escapes LIKE wildcards: folder names may contain % and _.
func likePrefix(p string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p)
}

// Heal realigns the gallery with the sources regardless of the cursor: drops assets
// outside every source, syncs the gallery trash with the file trash, reads what is
// missing or stale, regroups and sweeps orphaned derivatives.
//
// The pass can take hours (the first run over a big archive), so Heal only starts it in
// the background and returns: the tracker calls Heal from its LISTEN loop and under its
// per-library gate, and neither may be held for that long. Passes of one user still run
// one at a time (ix.passes).
func (ix *Indexer) Heal(ctx context.Context, userID pgtype.UUID) error {
	ps, err := ix.q.GetPhotoSettings(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if !ps.Enabled {
		return nil
	}
	ix.setScanning(userID, true)
	ix.bg.Add(1)
	go func() {
		defer ix.bg.Done()
		defer ix.setScanning(userID, false)
		if err := ix.pass(ctx, userID); err != nil && ctx.Err() == nil {
			log.Printf("discodrive: photos full pass user=%s: %v", db.UUIDString(userID), err)
		}
	}()
	return nil
}

// pass runs a full pass now, unless one of the user is running (that one then goes once
// more, see coalesce.Gate).
func (ix *Indexer) pass(ctx context.Context, userID pgtype.UUID) error {
	var err error
	ix.passes.Run(db.UUIDString(userID), func() { err = ix.fullPass(ctx, userID) })
	return err
}

// Reindex is the full pass after a change of the sources (or the gallery being switched
// on). The cursor starts at the present BEFORE the pass, so changes that arrive while it
// runs are picked up by the catch-up rather than lost (as for the music folder).
func (ix *Indexer) Reindex(ctx context.Context, userID pgtype.UUID) error {
	seq, err := ix.q.GetUserChangeSeq(ctx, userID)
	if err != nil {
		return err
	}
	if err := ix.SetCursor(ctx, userID, seq); err != nil {
		return err
	}
	ix.setScanning(userID, true)
	defer ix.setScanning(userID, false)
	return ix.pass(ctx, userID)
}

func (ix *Indexer) fullPass(ctx context.Context, userID pgtype.UUID) error {
	l := ix.userLock(userID)
	l.Lock()
	ids, err := ix.q.PrunePhotoAssetsOutsideSources(ctx, userID)
	if err == nil {
		ix.dropDerivatives(userID, ids)
		err = ix.q.SyncPhotoTrash(ctx, userID)
	}
	l.Unlock()
	if err != nil {
		return err
	}
	sources, err := ix.q.ListPhotoSources(ctx, userID)
	if err != nil {
		return err
	}
	for _, s := range sources {
		if _, err := ix.q.GetNode(ctx, s.FolderNodeID); errors.Is(err, pgx.ErrNoRows) {
			continue // trashed or purged: nothing live to read
		} else if err != nil {
			return err
		}
		if err := ix.indexStale(ctx, userID, s.FolderNodeID); err != nil {
			return err
		}
	}
	l.Lock()
	defer l.Unlock()
	ix.takeDirty(userID)
	if err := ix.regroup(ctx, userID); err != nil {
		return err
	}
	return ix.sweep(ctx, userID)
}

// AfterCatchUp regroups when the pass changed any asset and sweeps derivatives when the
// user purged something since the last sweep (purged nodes leave no change_log row: their
// assets go with the cascade, their derivative files would stay).
func (ix *Indexer) AfterCatchUp(ctx context.Context, userID pgtype.UUID) error {
	l := ix.userLock(userID)
	l.Lock()
	defer l.Unlock()
	if ix.takeDirty(userID) {
		if err := ix.regroup(ctx, userID); err != nil {
			ix.markDirty(userID)
			return err
		}
	}
	purged, err := ix.q.MaxPurgedSeq(ctx, userID)
	if err != nil {
		return err
	}
	ix.mu.Lock()
	done := ix.swept[userID] >= purged
	ix.mu.Unlock()
	if done {
		return nil
	}
	return ix.sweep(ctx, userID)
}

func (ix *Indexer) markDirty(userID pgtype.UUID) {
	ix.mu.Lock()
	ix.dirty[userID] = true
	ix.mu.Unlock()
}

func (ix *Indexer) takeDirty(userID pgtype.UUID) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	d := ix.dirty[userID]
	delete(ix.dirty, userID)
	return d
}

// sweep removes derivative files whose asset no longer exists. Files are listed before
// the ids are read, so a derivative written for an asset created meanwhile is never
// taken for an orphan.
func (ix *Indexer) sweep(ctx context.Context, userID pgtype.UUID) error {
	purged, err := ix.q.MaxPurgedSeq(ctx, userID)
	if err != nil {
		return err
	}
	dir := derivUserDir(ix.storageRoot, db.UUIDString(userID))
	shards, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	type file struct{ path, id string }
	var files []file
	for _, sh := range shards {
		if !sh.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(dir, sh.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			// <uuid>-<kind>.<ext>; anything else (a renderer's temp file) is not ours to judge.
			if len(name) > 37 && name[36] == '-' {
				files = append(files, file{filepath.Join(dir, sh.Name(), name), name[:36]})
			}
		}
	}
	if len(files) > 0 {
		ids, err := ix.q.ListPhotoAssetIDs(ctx, userID)
		if err != nil {
			return err
		}
		live := make(map[string]bool, len(ids))
		for _, id := range ids {
			live[db.UUIDString(id)] = true
		}
		for _, f := range files {
			if !live[f.id] {
				_ = os.Remove(f.path)
			}
		}
	}
	ix.mu.Lock()
	if ix.swept[userID] < purged {
		ix.swept[userID] = purged
	}
	ix.mu.Unlock()
	return nil
}
