package storage

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// ReconcileStats is the outcome of reconciling a tree with the database.
type ReconcileStats struct {
	Imported, Missing, Changed, Errors int
	ErrorText                          []string // at most maxErrorText
}

const maxErrorText = 5

func (st *ReconcileStats) fail(err error) {
	st.Errors++
	if len(st.ErrorText) < maxErrorText {
		st.ErrorText = append(st.ErrorText, err.Error())
	}
}

// Add accumulates another outcome into st.
func (st *ReconcileStats) Add(o ReconcileStats) {
	st.Imported += o.Imported
	st.Missing += o.Missing
	st.Changed += o.Changed
	st.Errors += o.Errors
	for _, e := range o.ErrorText {
		if len(st.ErrorText) < maxErrorText {
			st.ErrorText = append(st.ErrorText, e)
		}
	}
}

// ReconcileUser brings the database in line with files placed in or removed from the
// user's tree outside the service. It walks folder by folder, holding one folder's
// listing (disk and database) at a time, so memory follows the largest folder rather
// than the whole tree. It finds new entries, vanished ones, and files whose size on disk
// differs from the database; paths an upload or rename is changing are left alone.
func (s *FileService) ReconcileUser(ctx context.Context, userID pgtype.UUID) (ReconcileStats, error) {
	s.rescanMu.Lock()
	defer s.rescanMu.Unlock()
	var st ReconcileStats
	rel := db.UUIDString(userID)
	// Exists, not ReadDir: the root is listed once, inside reconcileDir, after its
	// database children are read.
	if ok, err := s.st.Exists(rel); err == nil && !ok {
		return st, nil // nothing stored for this user yet
	}
	s.reconcileDir(ctx, userID, pgtype.UUID{}, rel, &st)
	return st, ctx.Err()
}

// reconcileDir reconciles the folder at rel (parent is the invalid UUID for the user
// root) and descends into its subfolders.
func (s *FileService) reconcileDir(ctx context.Context, uid, parent pgtype.UUID, rel string, st *ReconcileStats) {
	if ctx.Err() != nil {
		return
	}
	var (
		live  []db.Node
		tombs []string
		err   error
	)
	if parent.Valid {
		live, err = s.q.ListNodeChildren(ctx, parent)
		if err == nil {
			tombs, err = s.q.ListTombstonedChildren(ctx, parent)
		}
	} else {
		live, err = s.q.ListRootNodes(ctx, uid)
		if err == nil {
			tombs, err = s.q.ListTombstonedRootChildren(ctx, uid)
		}
	}
	if err != nil {
		st.fail(fmt.Errorf("%s: %w", rel, err))
		return
	}
	items, err := s.st.ReadDir(rel)
	if err != nil {
		// Unreadable or vanished folder: its listing proves nothing, touch nothing in it.
		st.fail(fmt.Errorf("%s: %w", rel, err))
		return
	}

	byName := make(map[string]db.Node, len(live))
	for _, n := range live {
		byName[n.Name] = n
	}
	trashed := make(map[string]bool, len(tombs))
	for _, name := range tombs {
		trashed[name] = true
	}
	onDisk := make(map[string]bool, len(items))
	var subdirs []db.Node

	for _, it := range items {
		onDisk[it.Name] = true
		p := path.Join(rel, it.Name)
		if it.Kind == KindOther || s.busy.covers(p) {
			continue
		}
		n, known := byName[it.Name]
		switch {
		case !known:
			if trashed[it.Name] {
				continue // belongs to a trashed node — leave it alone
			}
			node, err := s.createDiscovered(ctx, uid, parent, p, it.Kind == KindDir)
			if err != nil {
				st.fail(fmt.Errorf("%s: %w", p, err))
				continue
			}
			st.Imported++
			if node.IsDir {
				subdirs = append(subdirs, node)
			}
		case n.IsDir != (it.Kind == KindDir):
			st.fail(fmt.Errorf("%s: a %s on disk but a %s in the database", p, kindName(it.Kind == KindDir), kindName(n.IsDir)))
		case n.IsDir:
			subdirs = append(subdirs, n)
		case !n.Size.Valid || n.Size.Int64 != it.Size:
			changed, err := s.markChanged(ctx, uid, n)
			if err != nil {
				st.fail(fmt.Errorf("%s: %w", p, err))
				continue
			}
			if changed {
				st.Changed++
			}
		}
	}

	for _, n := range live {
		if onDisk[n.Name] || s.busy.covers(n.DiskPath.String) {
			continue
		}
		if err := s.markMissing(ctx, uid, n); err != nil {
			st.fail(fmt.Errorf("%s: %w", n.DiskPath.String, err))
			continue
		}
		st.Missing++
	}

	for _, d := range subdirs {
		s.reconcileDir(ctx, uid, d.ID, d.DiskPath.String, st)
	}
}

func kindName(dir bool) string {
	if dir {
		return "folder"
	}
	return "file"
}

// markChanged re-hashes a file whose size on disk no longer matches the database and
// records the new content. No snapshot: the old content is already gone from disk.
// It reports false when a re-check shows another operation got there first.
func (s *FileService) markChanged(ctx context.Context, uid pgtype.UUID, n db.Node) (bool, error) {
	size, sha, err := s.hashFile(n.DiskPath.String)
	if err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	cur, err := qtx.GetNode(ctx, n.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if cur.DiskPath != n.DiskPath || cur.Version != n.Version {
		return false, nil // renamed or rewritten since the listing
	}
	updated, err := qtx.UpdateNodeContent(ctx, db.UpdateNodeContentParams{
		ID: n.ID, Size: int8val(size), ContentHash: text(sha), Mime: text(detectMime(n.Name)),
	})
	if err != nil {
		return false, err
	}
	if err := recordChange(ctx, qtx, uid, n.ID, "update", updated.Version); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
