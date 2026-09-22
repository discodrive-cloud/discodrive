package ebook

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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
func (ix *Indexer) IndexNode(ctx context.Context, userID, nodeID, diskPath string) error {
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

// ScanFolder walks every live file node under folderNodeID and indexes new or
// changed e-books, skipping books whose row is already up to date. Returns the
// count of files indexed (upserted).
func (ix *Indexer) ScanFolder(ctx context.Context, userID, folderNodeID string) (int, error) {
	folderUID, err := db.ParseUUID(folderNodeID)
	if err != nil {
		return 0, err
	}

	nodes, err := ix.q.ListFileNodesUnderFolder(ctx, folderUID)
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

		nodeIDStr := db.UUIDString(node.ID)

		// Change-gate: skip if the book row is newer than the node's modified_at,
		// and ALWAYS skip books whose metadata was edited by hand (their DB values
		// must not be clobbered by a file rescan, even if the file's mtime changed).
		existing, err := ix.q.GetBookByNode(ctx, node.ID)
		if err == nil {
			if existing.MetadataEdited {
				continue
			}
			if node.ModifiedAt.Valid && existing.UpdatedAt.Valid &&
				!existing.UpdatedAt.Time.Before(node.ModifiedAt.Time) {
				continue
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			// Non-fatal: skip this file, continue scanning.
			continue
		}

		if err := ix.IndexNode(ctx, userID, nodeIDStr, absPath); err != nil {
			// Non-fatal: skip unreadable files.
			continue
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
