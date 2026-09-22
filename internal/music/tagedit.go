package music

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/music/tagwrite"
	"discodrive/internal/storage"
)

var (
	ErrNotAudio         = errors.New("music: not an audio file")
	ErrReadOnlyFormat   = errors.New("music: format is read-only")
	ErrNotInMusicFolder = errors.New("music: node is not inside the user's music folder")
)

// TagInfo holds the result of a Read call.
type TagInfo struct {
	Tags     tagwrite.Tags
	HasCover bool
	Writable bool
	Suffix   string
}

// TagEditor ties tag reading/writing to the storage and indexing layers.
type TagEditor struct {
	q           *db.Queries
	files       *storage.FileService
	storageRoot string
}

// NewTagEditor constructs a TagEditor.
func NewTagEditor(q *db.Queries, files *storage.FileService, storageRoot string) *TagEditor {
	return &TagEditor{q: q, files: files, storageRoot: storageRoot}
}

// tagSuffix returns the lowercase extension without the leading dot.
func tagSuffix(path string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
}

// resolve verifies ownership, music-folder membership, and that the file is audio.
// Returns the node and absolute disk path on success.
func (e *TagEditor) resolve(ctx context.Context, userID, nodeID string) (db.Node, string, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Node{}, "", err
	}
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return db.Node{}, "", err
	}
	node, err := e.q.GetNodeForUser(ctx, db.GetNodeForUserParams{ID: nid, UserID: uid})
	if err != nil {
		return db.Node{}, "", err
	}
	if node.IsDir || !node.DiskPath.Valid {
		return db.Node{}, "", ErrNotAudio
	}
	if err := e.assertInMusicFolder(ctx, uid, node); err != nil {
		return db.Node{}, "", err
	}
	abs := filepath.Join(e.storageRoot, node.DiskPath.String)
	if !IsAudioFile(abs) {
		return db.Node{}, "", ErrNotAudio
	}
	return node, abs, nil
}

// assertInMusicFolder returns ErrNotInMusicFolder unless node's disk_path equals
// or is a descendant of the configured music folder's disk_path.
func (e *TagEditor) assertInMusicFolder(ctx context.Context, uid pgtype.UUID, node db.Node) error {
	ms, err := e.q.GetMusicSettings(ctx, uid)
	if err != nil || !ms.FolderNodeID.Valid {
		return ErrNotInMusicFolder
	}
	folder, err := e.q.GetNode(ctx, ms.FolderNodeID)
	if err != nil || !folder.DiskPath.Valid {
		return ErrNotInMusicFolder
	}
	base := strings.TrimSuffix(folder.DiskPath.String, "/") + "/"
	if node.DiskPath.String != folder.DiskPath.String &&
		!strings.HasPrefix(node.DiskPath.String, base) {
		return ErrNotInMusicFolder
	}
	return nil
}

// Read returns the current tags and cover metadata for a file.
func (e *TagEditor) Read(ctx context.Context, userID, nodeID string) (TagInfo, error) {
	_, abs, err := e.resolve(ctx, userID, nodeID)
	if err != nil {
		return TagInfo{}, err
	}
	ext := tagSuffix(abs)
	w, writable := tagwrite.For(ext)
	if w == nil {
		return TagInfo{}, ErrNotAudio
	}
	f, source, err := storage.NewLocalDisk(e.storageRoot).Pin(abs)
	if err != nil {
		return TagInfo{}, err
	}
	defer f.Close()
	tags, hasCover, err := w.Read(source)
	if err != nil {
		return TagInfo{}, err
	}
	return TagInfo{Tags: tags, HasCover: hasCover, Writable: writable, Suffix: ext}, nil
}

// Cover returns the embedded cover art bytes and MIME type.
func (e *TagEditor) Cover(ctx context.Context, userID, nodeID string) ([]byte, string, bool, error) {
	_, abs, err := e.resolve(ctx, userID, nodeID)
	if err != nil {
		return nil, "", false, err
	}
	w, _ := tagwrite.For(tagSuffix(abs))
	if w == nil {
		return nil, "", false, ErrNotAudio
	}
	f, source, err := storage.NewLocalDisk(e.storageRoot).Pin(abs)
	if err != nil {
		return nil, "", false, err
	}
	defer f.Close()
	data, mime, ok := w.Cover(source)
	return data, mime, ok, nil
}

// BulkFailure records the per-file error that occurred during a bulk folder edit.
type BulkFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// BulkResult summarises the outcome of a WriteFolder call.
type BulkResult struct {
	Affected int           `json:"affected"`
	Updated  int           `json:"updated"`
	Failed   []BulkFailure `json:"failed"`
}

// folderAudioNodes verifies that folderNodeID is owned by userID, is a directory,
// and lives inside the user's music folder; then returns all live audio file nodes
// recursively under it.
func (e *TagEditor) folderAudioNodes(ctx context.Context, userID, folderNodeID string) ([]db.Node, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, err
	}
	fid, err := db.ParseUUID(folderNodeID)
	if err != nil {
		return nil, err
	}
	folder, err := e.q.GetNodeForUser(ctx, db.GetNodeForUserParams{ID: fid, UserID: uid})
	if err != nil {
		return nil, err
	}
	if !folder.IsDir {
		return nil, ErrNotAudio
	}
	if err := e.assertInMusicFolder(ctx, uid, folder); err != nil {
		return nil, err
	}
	nodes, err := e.q.ListFileNodesUnderFolder(ctx, fid)
	if err != nil {
		return nil, err
	}
	out := nodes[:0]
	for _, n := range nodes {
		if n.DiskPath.Valid && IsAudioFile(filepath.Join(e.storageRoot, n.DiskPath.String)) {
			out = append(out, n)
		}
	}
	return out, nil
}

// CountFolderAudio returns the number of audio files recursively under folderNodeID.
// Useful for pre-flight confirm dialogs.
func (e *TagEditor) CountFolderAudio(ctx context.Context, userID, folderNodeID string) (int, error) {
	nodes, err := e.folderAudioNodes(ctx, userID, folderNodeID)
	if err != nil {
		return 0, err
	}
	return len(nodes), nil
}

// sanitizeBulkTags enforces the safety rules for bulk folder edits:
//   - Title and Track are per-song and are never written in bulk.
//   - A field is applied only when it carries a real value; an empty string or
//     zero number is treated as "leave unchanged", NOT "clear". This guarantees
//     a bulk edit can only SET the fields the user actually filled in and can
//     never silently wipe existing tags on other files (e.g. if a client sends
//     unchecked fields as empty values).
func sanitizeBulkTags(t tagwrite.Tags) tagwrite.Tags {
	t.Title = nil
	t.Track = nil
	dropEmptyStr := func(p *string) *string {
		if p == nil || *p == "" {
			return nil
		}
		return p
	}
	dropZeroInt := func(p *int) *int {
		if p == nil || *p == 0 {
			return nil
		}
		return p
	}
	t.Artist = dropEmptyStr(t.Artist)
	t.Album = dropEmptyStr(t.Album)
	t.AlbumArtist = dropEmptyStr(t.AlbumArtist)
	t.Genre = dropEmptyStr(t.Genre)
	t.Year = dropZeroInt(t.Year)
	t.Disc = dropZeroInt(t.Disc)
	return t
}

// WriteFolder applies the given tag/cover changes to every audio file recursively
// under folderNodeID. Bulk edits only SET non-empty fields and never clear or touch
// unspecified ones (see sanitizeBulkTags). Per-file errors are collected in
// BulkResult.Failed and do not abort the loop.
func (e *TagEditor) WriteFolder(ctx context.Context, userID, folderNodeID string, t tagwrite.Tags, cc tagwrite.CoverChange, cover *tagwrite.Cover) (BulkResult, error) {
	t = sanitizeBulkTags(t)

	nodes, err := e.folderAudioNodes(ctx, userID, folderNodeID)
	if err != nil {
		return BulkResult{}, err
	}
	res := BulkResult{Affected: len(nodes)}
	for _, n := range nodes {
		nodeID := db.UUIDString(n.ID)
		if err := e.Write(ctx, userID, nodeID, t, cc, cover); err != nil {
			res.Failed = append(res.Failed, BulkFailure{Path: n.DiskPath.String, Error: err.Error()})
			continue
		}
		res.Updated++
	}
	return res, nil
}

// Write applies tag and cover changes to a file, then commits via FileService
// and re-indexes the song in the music library.
//
// For the versioned mode (TagEditVersioning=true) a new sync version is created
// via PushByPath. For the in-place mode ReplaceContentInPlace is used instead.
// The temp file is always cleaned up regardless of outcome.
func (e *TagEditor) Write(ctx context.Context, userID, nodeID string, t tagwrite.Tags, cc tagwrite.CoverChange, cover *tagwrite.Cover) error {
	node, abs, err := e.resolve(ctx, userID, nodeID)
	if err != nil {
		return err
	}
	ext := tagSuffix(abs)
	w, writable := tagwrite.For(ext)
	if w == nil || !writable {
		return ErrReadOnlyFormat
	}

	// Copy the live file to a temp path so a failed Apply never corrupts it.
	tmp, err := os.CreateTemp("", "tagedit-*."+ext)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	src, err := storage.NewLocalDisk(e.storageRoot).OpenAbsolute(abs)
	if err != nil {
		tmp.Close()
		return err
	}
	_, copyErr := io.Copy(tmp, src)
	src.Close()
	tmp.Close()
	if copyErr != nil {
		return copyErr
	}

	if err := w.Apply(tmpPath, t, cc, cover); err != nil {
		return err
	}

	newBytes, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}

	// Determine save mode from per-user settings.
	uid, _ := db.ParseUUID(userID)
	ms, _ := e.q.GetMusicSettings(ctx, uid)

	if ms.TagEditVersioning {
		// Versioned mode: strip the "<userID>/" prefix to get a user-relative path.
		relPath := strings.TrimPrefix(node.DiskPath.String, userID+"/")
		if _, err := e.files.PushByPath(ctx, userID, relPath, nil, bytes.NewReader(newBytes)); err != nil {
			return err
		}
	} else {
		if _, err := e.files.ReplaceContentInPlace(ctx, userID, nodeID, bytes.NewReader(newBytes)); err != nil {
			return err
		}
	}

	// Re-read abs path: after PushByPath the disk content is updated in-place at the same path.
	ix := NewIndexer(e.q, e.storageRoot)
	return ix.IndexNode(ctx, userID, nodeID, abs)
}
