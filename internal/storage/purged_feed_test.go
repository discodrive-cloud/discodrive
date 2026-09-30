package storage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// purgeFixture builds p/docs/inner.txt plus an unrelated folder "other", trashes docs,
// then writes p/mid.txt (a live change between the soft and the hard delete).
type purgeFixture struct {
	fs                 *storage.FileService
	q                  *db.Queries
	pool               *pgxpool.Pool
	root               string // disk root
	userID             string
	uid                pgtype.UUID
	pID                string
	docsID, innerID    string
	docsPath, innerRel string
	cursorBefore       int64 // before the soft delete
	seqBeforeRemoval   int64 // after the soft delete, before the hard delete
}

func newPurgeFixture(t *testing.T) purgeFixture {
	t.Helper()
	ctx := context.Background()
	fs, q, pool, userID, root := setupFSPool(t, nil)
	uid, _ := db.ParseUUID(userID)

	p, err := fs.CreateFolder(ctx, userID, nil, "p")
	if err != nil {
		t.Fatalf("mkdir p: %v", err)
	}
	if _, err := fs.CreateFolder(ctx, userID, nil, "other"); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}
	pid := db.UUIDString(p.ID)
	docs, err := fs.CreateFolder(ctx, userID, &pid, "docs")
	if err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	did := db.UUIDString(docs.ID)
	inner, err := fs.Push(ctx, userID, &did, "inner.txt", nil, "i", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	before, err := q.GetUserChangeSeq(ctx, uid)
	if err != nil {
		t.Fatalf("seq: %v", err)
	}
	if err := fs.Delete(ctx, userID, did); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := fs.Push(ctx, userID, &pid, "mid.txt", nil, "i", strings.NewReader("m")); err != nil {
		t.Fatalf("push mid: %v", err)
	}
	mid, _ := q.GetUserChangeSeq(ctx, uid)
	return purgeFixture{
		fs: fs, q: q, pool: pool, root: root, userID: userID, uid: uid, pID: pid,
		docsID: did, innerID: db.UUIDString(inner.Node.ID),
		docsPath: docs.DiskPath.String, innerRel: inner.Node.DiskPath.String,
		cursorBefore: before, seqBeforeRemoval: mid,
	}
}

func (f purgeFixture) since(t *testing.T, seq int64) []db.ListChangesSinceRow {
	t.Helper()
	rows, err := f.q.ListChangesSince(context.Background(), db.ListChangesSinceParams{
		UserID: f.uid, Seq: seq, Lim: 10000, IncludePurged: true,
	})
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	return rows
}

func (f purgeFixture) purgedCount(t *testing.T, nodeID string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM purged_nodes WHERE node_id = $1::uuid`, nodeID).Scan(&n); err != nil {
		t.Fatalf("count purged: %v", err)
	}
	return n
}

func seqsOf(rows []db.ListChangesSinceRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.Seq
	}
	return out
}

// assertRemovedInFeed checks that a client resuming from cursorBefore learns that each
// of ids (id -> original path) is gone, from rows recorded by the hard delete itself.
func (f purgeFixture) assertRemovedInFeed(t *testing.T, ids map[string]string) map[string]db.ListChangesSinceRow {
	t.Helper()
	ctx := context.Background()
	rows := f.since(t, f.cursorBefore)
	for i := 1; i < len(rows); i++ {
		if rows[i].Seq <= rows[i-1].Seq {
			t.Fatalf("feed not in seq order: %d then %d", rows[i-1].Seq, rows[i].Seq)
		}
	}
	got := map[string]db.ListChangesSinceRow{}
	for _, r := range rows {
		got[db.UUIDString(r.NodeID)] = r
	}
	for id, path := range ids {
		r, ok := got[id]
		if !ok {
			t.Fatalf("feed since %d has no row for removed node %s (%s): %+v", f.cursorBefore, id, path, rows)
		}
		if !r.Deleted || r.Op != "delete" {
			t.Fatalf("row for %s: op=%q deleted=%v, want delete/true", path, r.Op, r.Deleted)
		}
		if r.Seq <= f.seqBeforeRemoval {
			t.Fatalf("row for %s has seq %d, want > %d (recorded by the removal)", path, r.Seq, f.seqBeforeRemoval)
		}
		if r.DiskPath.String != path {
			t.Fatalf("row for %s reports path %q", path, r.DiskPath.String)
		}
	}
	// The seqs are users.change_seq values, so a later change continues after them.
	now, _ := f.q.GetUserChangeSeq(ctx, f.uid)
	if last := rows[len(rows)-1].Seq; now != last {
		t.Fatalf("user change_seq %d, but the feed ends at %d", now, last)
	}

	// Paging one row at a time walks exactly the same rows (LIMIT across live change_log
	// rows and purged rows).
	var paged []int64
	cur := f.cursorBefore
	for {
		page, err := f.q.ListChangesSince(ctx, db.ListChangesSinceParams{UserID: f.uid, Seq: cur, Lim: 1, IncludePurged: true})
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		if len(page) == 0 {
			break
		}
		paged = append(paged, page[0].Seq)
		cur = page[0].Seq
	}
	if !slices.Equal(paged, seqsOf(rows)) {
		t.Fatalf("paged %v, full feed %v", paged, seqsOf(rows))
	}

	// A client already past the removal gets nothing more.
	if rest := f.since(t, now); len(rest) != 0 {
		t.Fatalf("feed after the removal returned %d rows: %+v", len(rest), rest)
	}
	// A pull from 0 gets them too: its client may know these ids from its own uploads.
	from0 := map[string]bool{}
	for _, r := range f.since(t, 0) {
		if r.Deleted && r.Op == "delete" {
			from0[db.UUIDString(r.NodeID)] = true
		}
	}
	for id, path := range ids {
		if !from0[id] {
			t.Fatalf("pull from 0 has no delete for removed node %s (%s)", id, path)
		}
	}
	return got
}

func TestPurgeReportsRemovedSubtreeInFeed(t *testing.T) {
	ctx := context.Background()
	f := newPurgeFixture(t)
	if err := f.fs.Purge(ctx, f.userID, f.docsID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	// A live change after the removal, so paging crosses both kinds of rows.
	if _, err := f.fs.Push(ctx, f.userID, &f.pID, "after.txt", nil, "i", strings.NewReader("a")); err != nil {
		t.Fatalf("push after: %v", err)
	}
	got := f.assertRemovedInFeed(t, map[string]string{f.docsID: f.docsPath, f.innerID: f.innerRel})
	// One removal records the whole subtree, parents first. (TrashGC removes expired
	// rows one by one in no set order, so there a child may come first.)
	if got[f.docsID].Seq > got[f.innerID].Seq {
		t.Fatalf("parent removal (%d) should precede the child's (%d)", got[f.docsID].Seq, got[f.innerID].Seq)
	}
}

func TestTrashGCReportsRemovedSubtreeInFeed(t *testing.T) {
	f := newPurgeFixture(t)
	if err := f.fs.TrashGC(context.Background(), 0); err != nil {
		t.Fatalf("gc: %v", err)
	}
	f.assertRemovedInFeed(t, map[string]string{f.docsID: f.docsPath, f.innerID: f.innerRel})
}

// A descendant trashed on its own before its folder is removed with the folder, and
// reported too.
func TestPurgeReportsSeparatelyTrashedDescendant(t *testing.T) {
	ctx := context.Background()
	fs, q, pool, userID, _ := setupFSPool(t, nil)
	uid, _ := db.ParseUUID(userID)
	docs, err := fs.CreateFolder(ctx, userID, nil, "docs")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	did := db.UUIDString(docs.ID)
	sub, err := fs.CreateFolder(ctx, userID, &did, "sub")
	if err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	sid := db.UUIDString(sub.ID)
	x, err := fs.Push(ctx, userID, &sid, "x.txt", nil, "i", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	before, _ := q.GetUserChangeSeq(ctx, uid)
	if err := fs.Delete(ctx, userID, sid); err != nil {
		t.Fatalf("delete sub: %v", err)
	}
	if err := fs.Delete(ctx, userID, did); err != nil {
		t.Fatalf("delete docs: %v", err)
	}
	mid, _ := q.GetUserChangeSeq(ctx, uid)
	if err := fs.Purge(ctx, userID, did); err != nil {
		t.Fatalf("purge: %v", err)
	}
	f := purgeFixture{fs: fs, q: q, pool: pool, userID: userID, uid: uid, cursorBefore: before, seqBeforeRemoval: mid}
	f.assertRemovedInFeed(t, map[string]string{
		did: docs.DiskPath.String, sid: sub.DiskPath.String, db.UUIDString(x.Node.ID): x.Node.DiskPath.String,
	})
}

func TestScopedFeedReportsRemovedNodes(t *testing.T) {
	ctx := context.Background()
	f := newPurgeFixture(t)
	if err := f.fs.Purge(ctx, f.userID, f.docsID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	scoped := func(rel string) map[string]db.ListChangesSinceUnderPrefixRow {
		rows, err := f.q.ListChangesSinceUnderPrefix(ctx, db.ListChangesSinceUnderPrefixParams{
			UserID: f.uid, Seq: f.cursorBefore, Lim: 10000, IncludePurged: true,
			Prefix: f.userID + "/" + rel + "/%",
		})
		if err != nil {
			t.Fatalf("scoped feed: %v", err)
		}
		out := map[string]db.ListChangesSinceUnderPrefixRow{}
		for _, r := range rows {
			out[db.UUIDString(r.NodeID)] = r
		}
		return out
	}
	inP := scoped("p")
	for _, id := range []string{f.docsID, f.innerID} {
		r, ok := inP[id]
		if !ok || !r.Deleted || r.Op != "delete" || r.Seq <= f.seqBeforeRemoval {
			t.Fatalf("scope p: removed node %s missing or not a delete: %+v (ok=%v)", id, r, ok)
		}
	}
	if other := scoped("other"); len(other) != 0 {
		t.Fatalf("scope other sees %d rows, want none: %+v", len(other), other)
	}
}

// A node created again at the removed node's path is a different node: its row comes
// after the removal rows and stays live.
func TestRecreatedNodeStaysLiveAfterRemovalRows(t *testing.T) {
	ctx := context.Background()
	f := newPurgeFixture(t)
	if err := f.fs.Purge(ctx, f.userID, f.docsID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	again, err := f.fs.CreateFolder(ctx, f.userID, &f.pID, "docs")
	if err != nil {
		t.Fatalf("mkdir docs again: %v", err)
	}
	rows := f.since(t, f.cursorBefore)
	last := map[string]db.ListChangesSinceRow{} // path -> last row for it
	for _, r := range rows {
		last[r.DiskPath.String] = r
	}
	r := last[f.docsPath]
	if db.UUIDString(r.NodeID) != db.UUIDString(again.ID) || r.Deleted {
		t.Fatalf("last row at %s = node %s deleted=%v, want live node %s",
			f.docsPath, db.UUIDString(r.NodeID), r.Deleted, db.UUIDString(again.ID))
	}
	if _, err := f.fs.NodeByPath(ctx, f.userID, "/p/docs"); err != nil {
		t.Fatalf("re-created docs not live: %v", err)
	}
}

// The name is taken again BEFORE the old node is purged: the purge reports only the old
// node, and the live one at the same path is untouched, in the tree and in the feed.
func TestPurgeAfterRecreateLeavesLiveNodeAlone(t *testing.T) {
	ctx := context.Background()
	f := newPurgeFixture(t)
	again, err := f.fs.CreateFolder(ctx, f.userID, &f.pID, "docs")
	if err != nil {
		t.Fatalf("mkdir docs again: %v", err)
	}
	newID := db.UUIDString(again.ID)
	if err := f.fs.Purge(ctx, f.userID, f.docsID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n := f.purgedCount(t, newID); n != 0 {
		t.Fatalf("live node recorded as purged (%d rows)", n)
	}
	if n := f.purgedCount(t, f.docsID); n != 1 {
		t.Fatalf("old node has %d purged rows, want 1", n)
	}
	var oldSeq int64
	for _, r := range f.since(t, f.cursorBefore) {
		switch db.UUIDString(r.NodeID) {
		case newID:
			if r.Deleted {
				t.Fatalf("live node reported deleted: %+v", r)
			}
		case f.docsID:
			oldSeq = r.Seq
		}
	}
	if oldSeq == 0 {
		t.Fatalf("old node's removal missing from the feed")
	}
	live, err := f.fs.NodeByPath(ctx, f.userID, "/p/docs")
	if err != nil || db.UUIDString(live.ID) != newID {
		t.Fatalf("live docs = %v, %v; want %s", db.UUIDString(live.ID), err, newID)
	}
}

// Race: a restore of inner.txt (out of the trashed docs, to the root) is still open when
// docs is purged. The purge must not report inner.txt as removed: it is live once the
// restore commits, and every client would delete a live file.
func TestPurgeDuringRestoreDoesNotReportRestoredNode(t *testing.T) {
	ctx := context.Background()
	f := newPurgeFixture(t)

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	// What Undelete does for a node whose parent is trashed: move it to the root, clear
	// the tombstone, record a create.
	if _, err := tx.Exec(ctx, `UPDATE nodes SET parent_id = NULL, deleted_at = NULL, trash_path = NULL,
		disk_path = user_id::text || '/inner.txt' WHERE id = $1::uuid`, f.innerID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := tx.Exec(ctx, `WITH b AS (UPDATE users SET change_seq = change_seq + 1 WHERE id = $1::uuid RETURNING change_seq)
		INSERT INTO change_log (user_id, node_id, seq, op, version) SELECT $1::uuid, $2::uuid, b.change_seq, 'create', 2 FROM b`,
		f.userID, f.innerID); err != nil {
		t.Fatalf("record restore: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- f.fs.Purge(ctx, f.userID, f.docsID) }()

	// Wait until the purge is blocked behind the open restore.
	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting int
		if err := f.pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting); err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("purge finished without waiting for the restore: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("purge never blocked on the restore")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit restore: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("purge: %v", err)
	}

	if _, err := f.fs.NodeByPath(ctx, f.userID, "/inner.txt"); err != nil {
		t.Fatalf("restored inner.txt is gone: %v", err)
	}
	if n := f.purgedCount(t, f.innerID); n != 0 {
		t.Fatalf("restored (live) inner.txt recorded as purged (%d rows): clients would delete it", n)
	}
	if n := f.purgedCount(t, f.docsID); n != 1 {
		t.Fatalf("docs has %d purged rows, want 1", n)
	}
	var last db.ListChangesSinceRow
	for _, r := range f.since(t, f.cursorBefore) {
		if db.UUIDString(r.NodeID) == f.innerID {
			last = r
		}
	}
	if last.Deleted || last.Op != "create" {
		t.Fatalf("last feed row for inner.txt = %+v, want the live create", last)
	}
}

// A trashed folder that still holds a live child (a create that raced the folder's soft
// delete) is not removed: the parent_id cascade would take the live child with it.
func TestPurgeKeepsTrashedFolderWithLiveChild(t *testing.T) {
	ctx := context.Background()
	f := newPurgeFixture(t)
	// Make inner.txt live while docs stays trashed.
	if _, err := f.pool.Exec(ctx, `UPDATE nodes SET deleted_at = NULL, trash_path = NULL WHERE id = $1::uuid`, f.innerID); err != nil {
		t.Fatalf("revive: %v", err)
	}
	trashDir := filepath.Join(f.root, ".trash", f.userID, f.docsID)
	if _, err := os.Stat(trashDir); err != nil {
		t.Fatalf("fixture: no trash dir for docs: %v", err)
	}
	if err := f.fs.Purge(ctx, f.userID, f.docsID); !errors.Is(err, storage.ErrKeptInTrash) {
		t.Fatalf("purge of a folder holding a live node = %v, want ErrKeptInTrash", err)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE id = $1::uuid`, f.innerID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("live child cascade-deleted (rows=%d, err=%v)", n, err)
	}
	if c := f.purgedCount(t, f.innerID) + f.purgedCount(t, f.docsID); c != 0 {
		t.Fatalf("%d purged rows recorded for a subtree that was kept", c)
	}
	// Bytes go only with rows actually removed: the kept folder keeps its trash.
	if _, err := os.Stat(trashDir); err != nil {
		t.Fatalf("kept folder lost its trash bytes: %v", err)
	}
}

// A client learns the ids of its own uploads from the push responses, while its cursor is
// still below their create rows. If another device trashes and purges such a file before
// this client pulls, the client must still get the removal.
func TestPurgeOfOwnUploadReachesPreUploadCursor(t *testing.T) {
	ctx := context.Background()
	f := newPurgeFixture(t)
	cursor, _ := f.q.GetUserChangeSeq(ctx, f.uid)
	up, err := f.fs.Push(ctx, f.userID, &f.pID, "mine.txt", nil, "i", strings.NewReader("u"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	id := db.UUIDString(up.Node.ID)
	if err := f.fs.Delete(ctx, f.userID, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := f.fs.Purge(ctx, f.userID, id); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var got *db.ListChangesSinceRow
	for _, r := range f.since(t, cursor) {
		if db.UUIDString(r.NodeID) == id {
			r := r
			got = &r
		}
	}
	if got == nil || !got.Deleted || got.Op != "delete" {
		t.Fatalf("pull from the pre-upload cursor %d: removal of the uploaded file = %+v, want a delete", cursor, got)
	}
}

// The same with cursor 0: a client that pushes before its first pull (after pairing or
// a reset) knows the uploaded file's id, so a pull from 0 must report its removal.
func TestPurgeOfOwnUploadReachesZeroCursor(t *testing.T) {
	ctx := context.Background()
	f := newPurgeFixture(t)
	up, err := f.fs.Push(ctx, f.userID, &f.pID, "mine.txt", nil, "i", strings.NewReader("u"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	id := db.UUIDString(up.Node.ID)
	if err := f.fs.Delete(ctx, f.userID, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := f.fs.Purge(ctx, f.userID, id); err != nil {
		t.Fatalf("purge: %v", err)
	}
	for _, r := range f.since(t, 0) {
		if db.UUIDString(r.NodeID) == id && r.Deleted && r.Op == "delete" {
			return
		}
	}
	t.Fatalf("pull from 0: no delete for the purged upload %s", id)
}

// waitForLockWait blocks until some backend waits on a lock, or fails the test. done
// reports the waiting operation finishing early (it never blocked).
func waitForLockWait(t *testing.T, pool *pgxpool.Pool, done <-chan error) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting); err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("operation finished without waiting on the open transaction (err=%v)", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation never blocked on the open transaction")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// liveChildren counts live nodes directly under parent.
func liveChildren(t *testing.T, pool *pgxpool.Pool, parent string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM nodes WHERE parent_id = $1::uuid AND deleted_at IS NULL`, parent).Scan(&n); err != nil {
		t.Fatalf("count children: %v", err)
	}
	return n
}

// A folder is being trashed (its transaction still open) while a mkdir and an upload
// target it. Both must wait and then fail: a node going live under a trashed folder
// would later be taken by the folder's purge.
func TestWriteIntoFolderBeingTrashedFails(t *testing.T) {
	ctx := context.Background()
	f := newPurgeFixture(t)
	other, err := f.fs.NodeByPath(ctx, f.userID, "/other")
	if err != nil {
		t.Fatalf("other: %v", err)
	}
	oid := db.UUIDString(other.ID)

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	qtx := f.q.WithTx(tx)
	// What Delete does before its commit.
	if _, err := qtx.LockLiveSubtreeDirs(ctx, db.LockLiveSubtreeDirsParams{UserID: f.uid, Prefix: other.DiskPath.String}); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if err := qtx.SoftDeleteSubtree(ctx, db.SoftDeleteSubtreeParams{UserID: f.uid, Prefix: other.DiskPath.String}); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	mkdir := make(chan error, 1)
	go func() {
		_, err := f.fs.CreateFolder(ctx, f.userID, &oid, "late")
		mkdir <- err
	}()
	waitForLockWait(t, f.pool, mkdir)
	upload := make(chan error, 1)
	go func() {
		_, err := f.fs.Push(ctx, f.userID, &oid, "late.txt", nil, "i", strings.NewReader("l"))
		upload <- err
	}()
	time.Sleep(300 * time.Millisecond) // let the upload reach its parent check too
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := <-mkdir; !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("mkdir into a trashed folder = %v, want ErrNotFound", err)
	}
	if err := <-upload; !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("upload into a trashed folder = %v, want ErrNotFound", err)
	}
	if n := liveChildren(t, f.pool, oid); n != 0 {
		t.Fatalf("%d live node(s) under the trashed folder", n)
	}
}

// A write into a folder is still open when the folder is deleted: the delete waits for
// it and trashes the new node with the rest of the folder.
func TestDeleteWaitsForWriteIntoFolder(t *testing.T) {
	ctx := context.Background()
	f := newPurgeFixture(t)
	other, err := f.fs.NodeByPath(ctx, f.userID, "/other")
	if err != nil {
		t.Fatalf("other: %v", err)
	}
	oid := db.UUIDString(other.ID)

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	qtx := f.q.WithTx(tx)
	// What CreateFolder does before its commit.
	if _, err := qtx.LockLiveParent(ctx, other.ID); err != nil {
		t.Fatalf("lock parent: %v", err)
	}
	child, err := qtx.CreateNode(ctx, db.CreateNodeParams{
		UserID: f.uid, ParentID: other.ID, Name: "c", IsDir: true,
		DiskPath: pgtype.Text{String: other.DiskPath.String + "/c", Valid: true},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- f.fs.Delete(ctx, f.userID, oid) }()
	waitForLockWait(t, f.pool, done)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n := liveChildren(t, f.pool, oid); n != 0 {
		t.Fatalf("child %s stayed live under the trashed folder", db.UUIDString(child.ID))
	}
	// And the folder can be purged whole.
	if err := f.fs.Purge(ctx, f.userID, oid); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n := f.purgedCount(t, db.UUIDString(child.ID)); n != 1 {
		t.Fatalf("child has %d purged rows, want 1", n)
	}
}
