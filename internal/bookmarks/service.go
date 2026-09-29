// Package bookmarks implements the server side of browser bookmark sync: a
// server-authoritative tree in browser_bookmarks with a per-user monotonic
// change cursor (users.bookmark_seq) and tombstones. Browser extensions push
// local changes and pull the changes feed; the web UI edits the same tree.
package bookmarks

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
	"discodrive/internal/fetchguard"
	"discodrive/internal/storage"
)

// Service mutates the bookmark tree. Client and Validate are the SSRF-guarded
// defaults; tests replace them to reach httptest servers on 127.0.0.1.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	st   storage.Storage

	Client   *http.Client
	Validate func(string) error
}

func NewService(pool *pgxpool.Pool, q *db.Queries, st storage.Storage) *Service {
	return &Service{
		pool:     pool,
		q:        q,
		st:       st,
		Client:   fetchguard.NewClient(0),
		Validate: fetchguard.ValidateURL,
	}
}

// Delete tombstones the node and its whole subtree in one transaction (the
// seq bump and the tombstone write must commit together, otherwise a client
// could observe the advanced cursor without the tombstones). Returns the
// number of tombstoned rows; 0 = unknown id / already deleted.
func (s *Service) Delete(ctx context.Context, userID, id pgtype.UUID) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	seq, err := qtx.NextBookmarkSeq(ctx, userID)
	if err != nil {
		return 0, err
	}
	n, err := qtx.TombstoneBrowserBookmarkTree(ctx, db.TombstoneBrowserBookmarkTreeParams{
		UserID: userID,
		ID:     id,
		Seq:    seq,
	})
	if err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// BulkItem is one node of a bulk import (initial sync from a browser).
type BulkItem struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	IsFolder bool    `json:"is_folder"`
	Title    string  `json:"title"`
	URL      string  `json:"url"`
	Position int32   `json:"position"`
}

// BulkImport upserts a whole tree in one transaction with a single seq (LWW:
// existing rows, including tombstones, are overwritten and revived). Returns
// the number of items and the new cursor.
func (s *Service) BulkImport(ctx context.Context, userID pgtype.UUID, items []BulkItem) (int, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	// Bumping the seq locks the user's row: every other mutation of this tree waits, so
	// the parents checked below cannot change before the import commits.
	seq, err := qtx.NextBookmarkSeq(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	ids, parents, err := parseBulk(items)
	if err != nil {
		return 0, 0, err
	}
	if err := checkBulkParents(ctx, qtx, userID, items, ids, parents); err != nil {
		return 0, 0, err
	}
	for i, it := range items {
		url := it.URL
		if it.IsFolder {
			url = ""
		}
		n, err := qtx.UpsertBrowserBookmarkAt(ctx, db.UpsertBrowserBookmarkAtParams{
			ID:       ids[i],
			UserID:   userID,
			ParentID: parents[i],
			IsFolder: it.IsFolder,
			Title:    it.Title,
			Url:      url,
			Position: it.Position,
			Seq:      seq,
		})
		if err != nil {
			return 0, 0, fmt.Errorf("item %d: %w", i, err)
		}
		if n == 0 {
			return 0, 0, fmt.Errorf("%w: item %d: id %q is already in use", ErrInvalidImport, i, it.ID)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return len(items), seq, nil
}

// ErrInvalidImport marks a bulk import refused for its content (bad ids or parents).
var ErrInvalidImport = errors.New("invalid bookmark import")

// parseBulk parses the ids and parent ids of an import; ids must be unique.
func parseBulk(items []BulkItem) (ids, parents []pgtype.UUID, err error) {
	ids = make([]pgtype.UUID, len(items))
	parents = make([]pgtype.UUID, len(items))
	seen := make(map[pgtype.UUID]struct{}, len(items))
	for i, it := range items {
		if ids[i], err = db.ParseUUID(it.ID); err != nil {
			return nil, nil, fmt.Errorf("%w: item %d: bad id %q", ErrInvalidImport, i, it.ID)
		}
		if _, dup := seen[ids[i]]; dup {
			return nil, nil, fmt.Errorf("%w: item %d: duplicate id %q", ErrInvalidImport, i, it.ID)
		}
		seen[ids[i]] = struct{}{}
		if it.ParentID != nil && *it.ParentID != "" {
			if parents[i], err = db.ParseUUID(*it.ParentID); err != nil {
				return nil, nil, fmt.Errorf("%w: item %d: bad parent_id %q", ErrInvalidImport, i, *it.ParentID)
			}
		}
	}
	return ids, parents, nil
}

// checkBulkParents refuses an import whose tree would be broken once written: a
// parent must be a folder, either in the import itself or already the user's (a
// tombstoned one too: the import may be reviving it), and following parents upward
// from any imported node, through the tree as it will be after the import, must end
// at the top level instead of looping. A cycle made a node undeletable and invisible,
// and hung the recursive tree queries.
func checkBulkParents(ctx context.Context, q *db.Queries, userID pgtype.UUID, items []BulkItem, ids, parents []pgtype.UUID) error {
	inBatch := make(map[pgtype.UUID]int, len(items))
	for i, id := range ids {
		inBatch[id] = i
	}
	var external []pgtype.UUID
	for i, p := range parents {
		if !p.Valid {
			continue
		}
		if p == ids[i] {
			return fmt.Errorf("%w: item %d is its own parent", ErrInvalidImport, i)
		}
		if j, ok := inBatch[p]; ok {
			if !items[j].IsFolder {
				return fmt.Errorf("%w: item %d: parent is not a folder", ErrInvalidImport, i)
			}
			continue
		}
		external = append(external, p)
	}

	// The tree after the import: stored nodes (the external parents and all their
	// ancestors), overlaid with the imported nodes' new parents.
	parentOf := map[pgtype.UUID]pgtype.UUID{}
	folder := map[pgtype.UUID]bool{}
	if len(external) > 0 {
		rows, err := q.BrowserBookmarkAncestors(ctx, db.BrowserBookmarkAncestorsParams{UserID: userID, Ids: external})
		if err != nil {
			return err
		}
		for _, r := range rows {
			parentOf[r.ID] = r.ParentID
			folder[r.ID] = r.IsFolder
		}
		for i, p := range parents {
			if _, ok := inBatch[p]; ok || !p.Valid {
				continue
			}
			if isFolder, known := folder[p]; !known || !isFolder {
				return fmt.Errorf("%w: item %d: parent is not one of your folders", ErrInvalidImport, i)
			}
		}
	}
	for i, id := range ids {
		parentOf[id] = parents[i]
	}

	// Walk up from every imported node; nodes proven to reach the top are memoized,
	// so the whole check is linear in the size of the tree.
	const (
		onPath = 1
		ok     = 2
	)
	state := make(map[pgtype.UUID]int, len(parentOf))
	var path []pgtype.UUID
	for i, id := range ids {
		path = path[:0]
		for cur := id; cur.Valid; {
			if state[cur] == ok {
				break
			}
			if state[cur] == onPath {
				return fmt.Errorf("%w: item %d: parent chain forms a cycle", ErrInvalidImport, i)
			}
			state[cur] = onPath
			path = append(path, cur)
			next, known := parentOf[cur]
			if !known {
				break // an orphan stored before: it ends the chain like the top level
			}
			cur = next
		}
		for _, n := range path {
			state[n] = ok
		}
	}
	return nil
}
