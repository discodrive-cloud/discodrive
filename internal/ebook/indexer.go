package ebook

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/coalesce"
	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// Indexer upserts book rows (and their authors/tags/covers) for a given user.
type Indexer struct {
	q           *db.Queries
	storageRoot string
}

// NewIndexer creates an Indexer backed by the given query set and storage root.
func NewIndexer(q *db.Queries, storageRoot string) *Indexer {
	return &Indexer{q: q, storageRoot: storageRoot}
}

// IndexNode reads metadata from the e-book at diskPath and upserts the book
// (plus its authors, tags, and cover) for the given user + file node.
// The operation is idempotent.
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

	// Derive sort title — fall back to lowercase of title if missing.
	sortTitle := meta.SortTitle
	if sortTitle == "" {
		sortTitle = strings.ToLower(meta.Title)
	}

	// File size from disk.
	var sizePg pgtype.Int8
	if fi, serr := file.Stat(); serr == nil {
		sizePg = pgtype.Int8{Int64: fi.Size(), Valid: true}
	}

	// Optional nullable fields.
	var seriesIndexPg pgtype.Float4
	if meta.SeriesIndex != 0 {
		seriesIndexPg = pgtype.Float4{Float32: float32(meta.SeriesIndex), Valid: true}
	}

	book, err := ix.q.UpsertBook(ctx, db.UpsertBookParams{
		UserID:        uid,
		NodeID:        nid,
		Title:         meta.Title,
		SortTitle:     sortTitle,
		Language:      pgtype.Text{String: meta.Language, Valid: meta.Language != ""},
		Isbn:          pgtype.Text{String: meta.ISBN, Valid: meta.ISBN != ""},
		Description:   pgtype.Text{String: meta.Description, Valid: meta.Description != ""},
		Publisher:     pgtype.Text{String: meta.Publisher, Valid: meta.Publisher != ""},
		PublishedDate: pgtype.Text{String: meta.Date, Valid: meta.Date != ""},
		Series:        pgtype.Text{String: meta.Series, Valid: meta.Series != ""},
		SeriesIndex:   seriesIndexPg,
		Format:        meta.Format,
		ContentType:   meta.ContentType,
		Size:          sizePg,
	})
	if err != nil {
		return err
	}

	// Replace authors (clear + re-insert for idempotency).
	if err := ix.q.ClearBookAuthors(ctx, book.ID); err != nil {
		return err
	}
	for _, a := range meta.Authors {
		sortName := a.SortName
		if sortName == "" {
			sortName = strings.ToLower(a.Name)
		}
		if err := ix.q.InsertBookAuthor(ctx, db.InsertBookAuthorParams{
			BookID:   book.ID,
			Name:     a.Name,
			SortName: sortName,
		}); err != nil {
			return err
		}
	}

	// Replace tags (clear + re-insert for idempotency).
	if err := ix.q.ClearBookTags(ctx, book.ID); err != nil {
		return err
	}
	for _, tag := range meta.Tags {
		if err := ix.q.InsertBookTag(ctx, db.InsertBookTagParams{
			BookID: book.ID,
			Tag:    tag,
		}); err != nil {
			return err
		}
	}

	// Cache extracted cover to disk and record the relative path.
	if len(meta.CoverData) > 0 {
		bookIDStr := db.UUIDString(book.ID)
		relPath, werr := WriteCover(ix.storageRoot, bookIDStr, meta.CoverData, meta.CoverType)
		if werr == nil {
			_ = ix.q.SetBookCoverPath(ctx, db.SetBookCoverPathParams{
				ID:        book.ID,
				CoverPath: pgtype.Text{String: relPath, Valid: true},
			})
		}
	}

	return nil
}

// scans keeps one ebook scan per user and folder in flight: the scan button, a folder
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

// scanFolder walks every live file node under folderNodeID and indexes new or
// changed e-books, skipping books whose row is already up to date. Returns the
// count of files indexed (upserted).
func (ix *Indexer) scanFolder(ctx context.Context, userID, folderNodeID string) (int, error) {
	folderUID, err := db.ParseUUID(folderNodeID)
	if err != nil {
		return 0, err
	}
	nodes, err := ix.q.ListStaleBookNodes(ctx, folderUID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, node := range nodes {
		if !node.DiskPath.Valid {
			continue
		}
		absPath := filepath.Join(ix.storageRoot, node.DiskPath.String)
		if !IsBookFile(absPath) {
			continue
		}
		if err := ix.IndexNode(ctx, userID, db.UUIDString(node.ID), absPath); err != nil {
			continue // non-fatal: skip unreadable files
		}
		count++
	}
	return count, nil
}

// RemoveNode deletes the book for a node and removes its cached cover file.
func (ix *Indexer) RemoveNode(ctx context.Context, nodeID string) error {
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return err
	}

	book, err := ix.q.GetBookByNode(ctx, nid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}

	// Best-effort removal of cached cover.
	if book.CoverPath.Valid && book.CoverPath.String != "" {
		_ = RemoveCover(ix.storageRoot, book.CoverPath.String)
	}

	return ix.q.DeleteBookByNode(ctx, nid)
}

func (ix *Indexer) Name() string { return "ebooks" }

func (ix *Indexer) State(ctx context.Context, userID pgtype.UUID) (string, int64, bool, error) {
	es, err := ix.q.GetEbookSettings(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, nil
	} else if err != nil {
		return "", 0, false, err
	}
	if !es.Enabled || !es.FolderNodeID.Valid {
		return "", 0, false, nil
	}
	// Trashed or not: when the library folder itself goes to the trash, its own "delete"
	// in the log must still be matched against its path to drop everything under it.
	folder, err := ix.q.GetNodeAnyState(ctx, es.FolderNodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, nil // purged: the cascade already removed its songs
	} else if err != nil {
		return "", 0, false, err
	}
	return folder.DiskPath.String, es.IndexedSeq, true, nil
}

func (ix *Indexer) SetCursor(ctx context.Context, userID pgtype.UUID, seq int64) error {
	return ix.q.SetEbookIndexedSeq(ctx, db.SetEbookIndexedSeqParams{UserID: userID, IndexedSeq: seq})
}

func (ix *Indexer) Accepts(diskPath string) bool { return IsBookFile(diskPath) }

func (ix *Indexer) Index(ctx context.Context, userID, nodeID, diskPath string) error {
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return err
	}
	if b, err := ix.q.GetBookByNode(ctx, nid); err == nil && b.MetadataEdited {
		return nil // hand-edited metadata is never overwritten by the file
	}
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
	covers, err := ix.q.DeleteBooksUnderPath(ctx, db.DeleteBooksUnderPathParams{UserID: userID, Prefix: likePrefix(dirPath)})
	for _, c := range covers {
		if c.Valid && c.String != "" {
			_ = RemoveCover(ix.storageRoot, c.String)
		}
	}
	return err
}

// likePrefix escapes LIKE wildcards: folder names may contain % and _.
func likePrefix(p string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p)
}

// Heal is music.Indexer.Heal for the e-book library.
func (ix *Indexer) Heal(ctx context.Context, userID pgtype.UUID) error {
	es, err := ix.q.GetEbookSettings(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if !es.Enabled || !es.FolderNodeID.Valid {
		return nil
	}
	if _, err := ix.q.GetNode(ctx, es.FolderNodeID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	covers, err := ix.q.PruneBooksOutsideFolder(ctx, db.PruneBooksOutsideFolderParams{UserID: userID, FolderID: es.FolderNodeID})
	for _, c := range covers {
		if c.Valid && c.String != "" {
			_ = RemoveCover(ix.storageRoot, c.String)
		}
	}
	if err != nil {
		return err
	}
	_, err = ix.ScanFolder(ctx, db.UUIDString(userID), db.UUIDString(es.FolderNodeID))
	return err
}
