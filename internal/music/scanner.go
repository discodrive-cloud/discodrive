// Package music provides audio-file tag scanning and database indexing for the
// OpenSubsonic music service.
package music

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/dhowden/tag"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/coalesce"
	"discodrive/internal/db"
	"discodrive/internal/music/tagwrite"
	"discodrive/internal/storage"
)

// audioExtensions maps lowercase file extensions (without dot) to MIME content types.
var audioExtensions = map[string]string{
	"mp3":  "audio/mpeg",
	"flac": "audio/flac",
	"m4a":  "audio/mp4",
	"ogg":  "audio/ogg",
}

// Meta holds tags extracted from one audio file.
type Meta struct {
	Title, Artist, Album, Genre, Suffix, ContentType, MBID string
	Track, Disc, Year                                      int
}

// ReadMeta extracts tags from an audio file at path. Suffix and ContentType are
// derived from the file extension. Title falls back to the filename (without
// extension) when the tag is empty.
//
// Tags are read primarily through the tagwrite package: its mp3 (bogem) and flac
// (go-flac) readers parse ID3v2.3 UTF-16 frames that dhowden/tag silently returns
// empty for — without this, such files index as "Unknown Artist/Album". dhowden
// is used as a per-field fallback for anything the primary reader misses.
func ReadMeta(path string) (Meta, error) { return readMeta(path, path) }

// ReadStoredMeta pins the file before handing it to path-based parsers.
func ReadStoredMeta(root, path string) (Meta, error) {
	f, source, err := storage.NewLocalDisk(root).Pin(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()
	return readMeta(path, source)
}

func readMeta(path, source string) (Meta, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	ct, ok := audioExtensions[ext]
	if !ok {
		return Meta{}, errors.New("music: unsupported audio format: " + ext)
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	meta := Meta{Title: base, Suffix: ext, ContentType: ct}

	if w, _ := tagwrite.For(ext); w != nil {
		if t, _, err := w.Read(source); err == nil {
			if t.Title != nil && *t.Title != "" {
				meta.Title = *t.Title
			}
			if t.Artist != nil {
				meta.Artist = *t.Artist
			}
			if t.Album != nil {
				meta.Album = *t.Album
			}
			if t.Genre != nil {
				meta.Genre = *t.Genre
			}
			if t.Year != nil {
				meta.Year = *t.Year
			}
			if t.Track != nil {
				meta.Track = *t.Track
			}
			if t.Disc != nil {
				meta.Disc = *t.Disc
			}
		}
	}

	// Fallback: fill any field the primary reader left empty from dhowden/tag.
	if meta.Artist == "" || meta.Album == "" || meta.Genre == "" || meta.Year == 0 || meta.Title == base {
		if f, ferr := os.Open(source); ferr == nil {
			defer f.Close()
			if m, merr := tag.ReadFrom(f); merr == nil {
				if meta.Artist == "" {
					meta.Artist = m.Artist()
				}
				if meta.Album == "" {
					meta.Album = m.Album()
				}
				if meta.Genre == "" {
					meta.Genre = m.Genre()
				}
				if meta.Year == 0 {
					meta.Year = m.Year()
				}
				if meta.Title == base && m.Title() != "" {
					meta.Title = m.Title()
				}
				if meta.Track == 0 {
					if tn, _ := m.Track(); tn > 0 {
						meta.Track = tn
					}
				}
				if meta.Disc == 0 {
					if dn, _ := m.Disc(); dn > 0 {
						meta.Disc = dn
					}
				}
			}
		}
	}

	return meta, nil
}

// IsAudioFile reports whether the file at path has a supported audio extension.
func IsAudioFile(path string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	_, ok := audioExtensions[ext]
	return ok
}

// Indexer upserts artist → album → song rows for a given user.
type Indexer struct {
	q           *db.Queries
	storageRoot string
}

// NewIndexer creates an Indexer backed by the given query set and storage root.
func NewIndexer(q *db.Queries, storageRoot string) *Indexer {
	return &Indexer{q: q, storageRoot: storageRoot}
}

// IndexNode reads tags for the file at diskPath and upserts the song (and its
// artist/album) for the given user + file node. The operation is idempotent.
func (ix *Indexer) IndexNode(ctx context.Context, userID, nodeID, diskPath string) (err error) {
	defer recoverParse(&err)
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return err
	}
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return err
	}

	file, source, err := storage.NewLocalDisk(ix.storageRoot).Pin(diskPath)
	if err != nil {
		return err
	}
	defer file.Close()
	meta, err := readMeta(diskPath, source)
	if err != nil {
		return err
	}

	// Duration is stored as a valid value even when the probe fails (0 = "probed,
	// unknown"): NULL is reserved for rows that were never probed, so the scan
	// change-gate can tell "needs enrichment" from "nothing more to extract"
	// and does not re-index unprobeable files forever.
	dur, br := ProbeAudio(source, meta.Suffix)
	durPg := pgtype.Int4{Int32: int32(dur), Valid: true}
	brPg := pgtype.Int4{}
	if br > 0 {
		brPg = pgtype.Int4{Int32: int32(br), Valid: true}
	}

	// Resolve artist name (fall back to "Unknown Artist").
	artistName := meta.Artist
	if artistName == "" {
		artistName = "Unknown Artist"
	}
	sortName := strings.ToLower(artistName)

	artist, err := ix.q.UpsertArtist(ctx, db.UpsertArtistParams{
		UserID:   uid,
		Name:     artistName,
		SortName: sortName,
	})
	if err != nil {
		return err
	}

	// Resolve album name (fall back to "Unknown Album").
	albumName := meta.Album
	if albumName == "" {
		albumName = "Unknown Album"
	}
	yearPg := pgtype.Int4{}
	if meta.Year > 0 {
		yearPg = pgtype.Int4{Int32: int32(meta.Year), Valid: true}
	}
	genrePg := pgtype.Text{}
	if meta.Genre != "" {
		genrePg = pgtype.Text{String: meta.Genre, Valid: true}
	}

	album, err := ix.q.UpsertAlbum(ctx, db.UpsertAlbumParams{
		UserID:   uid,
		ArtistID: artist.ID,
		Name:     albumName,
		Year:     yearPg,
		Genre:    genrePg,
	})
	if err != nil {
		return err
	}

	// Get file info for size.
	var sizePg pgtype.Int8
	if fi, serr := file.Stat(); serr == nil {
		sizePg = pgtype.Int8{Int64: fi.Size(), Valid: true}
	}

	trackPg := pgtype.Int4{}
	if meta.Track > 0 {
		trackPg = pgtype.Int4{Int32: int32(meta.Track), Valid: true}
	}
	discPg := pgtype.Int4{}
	if meta.Disc > 0 {
		discPg = pgtype.Int4{Int32: int32(meta.Disc), Valid: true}
	}
	suffixPg := pgtype.Text{String: meta.Suffix, Valid: meta.Suffix != ""}
	ctPg := pgtype.Text{String: meta.ContentType, Valid: meta.ContentType != ""}

	song, err := ix.q.UpsertSong(ctx, db.UpsertSongParams{
		UserID:      uid,
		AlbumID:     album.ID,
		ArtistID:    artist.ID,
		NodeID:      nid,
		Title:       meta.Title,
		Track:       trackPg,
		Disc:        discPg,
		Duration:    durPg,
		Bitrate:     brPg,
		Suffix:      suffixPg,
		ContentType: ctPg,
		Size:        sizePg,
		Genre:       genrePg,
	})
	if err != nil {
		return err
	}
	_ = song

	// Update album's song count.
	if err := ix.q.RefreshAlbumSongCount(ctx, album.ID); err != nil {
		return err
	}

	// Resolve cover art: prefer a sibling image file, fall back to this node.
	dir := filepath.Dir(diskPath)
	if coverPath, ok := ResolveCoverPath(dir); ok {
		// Try to find the node for the cover file in the DB.
		if coverNodeID, cok := ix.findNodeIDByDiskPath(ctx, uid, coverPath); cok {
			_ = ix.q.SetAlbumCover(ctx, db.SetAlbumCoverParams{
				ID:       album.ID,
				CoverArt: pgtype.Text{String: coverNodeID, Valid: true},
			})
			// Set artist cover only if not yet set.
			if !artist.CoverArt.Valid {
				_ = ix.q.SetArtistCover(ctx, db.SetArtistCoverParams{
					ID:       artist.ID,
					CoverArt: pgtype.Text{String: coverNodeID, Valid: true},
				})
			}
		}
	} else if !album.CoverArt.Valid {
		// Fall back: use the song's own node as embedded-art source.
		_ = ix.q.SetAlbumCover(ctx, db.SetAlbumCoverParams{
			ID:       album.ID,
			CoverArt: pgtype.Text{String: nodeID, Valid: true},
		})
	}

	return nil
}

// findNodeIDByDiskPath looks up a node by its disk_path (relative to storageRoot).
// Returns the UUID string and true if found, empty string and false otherwise.
func (ix *Indexer) findNodeIDByDiskPath(ctx context.Context, userID pgtype.UUID, absPath string) (string, bool) {
	rel, err := filepath.Rel(ix.storageRoot, absPath)
	if err != nil {
		return "", false
	}
	node, err := ix.q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{
		UserID: userID,
		Path:   rel,
	})
	if err != nil {
		return "", false
	}
	return db.UUIDString(node.ID), true
}

// RemoveNode deletes the song for a node. Album/artist orphan cleanup is
// best-effort and handled via ON DELETE cascade from nodes.
func (ix *Indexer) RemoveNode(ctx context.Context, nodeID string) error {
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return err
	}
	return ix.q.DeleteSongByNode(ctx, nid)
}

// scans keeps one music scan per user and folder in flight: the scan button, a folder
// switch and the change-log catch-up can all ask for one at once.
var scans coalesce.Gate

// ScanFolder indexes the folder, or — when a scan of it is already running — asks that
// scan for one more pass and returns (0, nil) at once.
func (ix *Indexer) ScanFolder(ctx context.Context, userID, folderNodeID string) (n int, err error) {
	scans.Run(userID+"/"+folderNodeID, func() {
		k, e := ix.scanFolder(ctx, userID, folderNodeID)
		n, err = n+k, e
	})
	return n, err
}

// scanFolder walks every live audio file under folderNodeID and indexes new or
// changed files, skipping songs whose row is already up to date. Returns the
// count of files indexed (upserted).
func (ix *Indexer) scanFolder(ctx context.Context, userID, folderNodeID string) (int, error) {
	folderUID, err := db.ParseUUID(folderNodeID)
	if err != nil {
		return 0, err
	}
	nodes, err := ix.q.ListStaleSongNodes(ctx, folderUID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, node := range nodes {
		if !node.DiskPath.Valid {
			continue
		}
		absPath := filepath.Join(ix.storageRoot, node.DiskPath.String)
		if !IsAudioFile(absPath) {
			continue
		}
		if err := ix.IndexNode(ctx, userID, db.UUIDString(node.ID), absPath); err != nil {
			continue // non-fatal: skip unreadable files
		}
		count++
	}
	return count, nil
}

func (ix *Indexer) Name() string { return "music" }

func (ix *Indexer) State(ctx context.Context, userID pgtype.UUID) (string, int64, bool, error) {
	ms, err := ix.q.GetMusicSettings(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, nil
	} else if err != nil {
		return "", 0, false, err
	}
	if !ms.Enabled || !ms.FolderNodeID.Valid {
		return "", 0, false, nil
	}
	// Trashed or not: when the library folder itself goes to the trash, its own "delete"
	// in the log must still be matched against its path to drop everything under it.
	folder, err := ix.q.GetNodeAnyState(ctx, ms.FolderNodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, nil // purged: the cascade already removed its songs
	} else if err != nil {
		return "", 0, false, err
	}
	return folder.DiskPath.String, ms.IndexedSeq, true, nil
}

func (ix *Indexer) SetCursor(ctx context.Context, userID pgtype.UUID, seq int64) error {
	return ix.q.SetMusicIndexedSeq(ctx, db.SetMusicIndexedSeqParams{UserID: userID, IndexedSeq: seq})
}

func (ix *Indexer) Accepts(diskPath string) bool { return IsAudioFile(diskPath) }

func (ix *Indexer) Index(ctx context.Context, userID, nodeID, diskPath string) error {
	return ix.IndexNode(ctx, userID, nodeID, filepath.Join(ix.storageRoot, diskPath))
}

func (ix *Indexer) Remove(ctx context.Context, nodeID string) error {
	return ix.RemoveNode(ctx, nodeID)
}

func (ix *Indexer) IndexUnder(ctx context.Context, userID, dirNodeID string) error {
	_, err := ix.ScanFolder(ctx, userID, dirNodeID)
	return err
}

func (ix *Indexer) RemoveUnder(ctx context.Context, userID pgtype.UUID, dirPath string) error {
	return ix.q.DeleteSongsUnderPath(ctx, db.DeleteSongsUnderPathParams{UserID: userID, Prefix: likePrefix(dirPath)})
}

// likePrefix escapes LIKE wildcards: folder names may contain % and _.
func likePrefix(p string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p)
}
