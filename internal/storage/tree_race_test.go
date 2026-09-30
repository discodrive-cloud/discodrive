package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// assertTreeConsistent checks what concurrent writes must never break: every live node
// sits under a live parent at exactly parent path + "/" + name, and every live node
// exists on disk at its disk_path.
func assertTreeConsistent(t *testing.T, pool *pgxpool.Pool, userID, root string) {
	t.Helper()
	ctx := context.Background()
	var bad int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM nodes c LEFT JOIN nodes p ON p.id = c.parent_id
		WHERE c.user_id = $1::uuid AND c.deleted_at IS NULL AND (
		      (c.parent_id IS NULL AND c.disk_path <> c.user_id::text || '/' || c.name)
		   OR (c.parent_id IS NOT NULL AND (p.deleted_at IS NOT NULL OR c.disk_path <> p.disk_path || '/' || c.name)))`,
		userID).Scan(&bad); err != nil {
		t.Fatalf("consistency query: %v", err)
	}
	if bad != 0 {
		rows, _ := pool.Query(ctx, `SELECT disk_path, name, deleted_at IS NOT NULL FROM nodes WHERE user_id = $1::uuid ORDER BY disk_path`, userID)
		var dump []string
		for rows.Next() {
			var p, n string
			var del bool
			_ = rows.Scan(&p, &n, &del)
			dump = append(dump, fmt.Sprintf("%s (%s, trashed=%v)", p, n, del))
		}
		rows.Close()
		t.Fatalf("%d live node(s) with a stale path or a trashed parent:\n%s", bad, strings.Join(dump, "\n"))
	}
	rows, err := pool.Query(ctx, `SELECT disk_path FROM nodes WHERE user_id = $1::uuid AND deleted_at IS NULL`, userID)
	if err != nil {
		t.Fatalf("live nodes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("live node %s missing on disk: %v", p, err)
		}
	}
}

// race runs a and b at the same moment and returns their errors.
func race(a, b func() error) (error, error) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	var ea, eb error
	wg.Add(2)
	go func() { defer wg.Done(); <-start; ea = a() }()
	go func() { defer wg.Done(); <-start; eb = b() }()
	close(start)
	wg.Wait()
	return ea, eb
}

// An overwrite of g/p/x.txt races a rename of p (or of g). Both must succeed, in some
// order: no deadlock error, and x.txt must not be left at a path that no longer exists.
func TestOverwriteRacingFolderRename(t *testing.T) {
	for _, renamed := range []string{"parent", "grandparent"} {
		t.Run(renamed, func(t *testing.T) {
			ctx := context.Background()
			fs, _, pool, userID, root := setupFSPool(t, nil)
			g, err := fs.CreateFolder(ctx, userID, nil, "g")
			if err != nil {
				t.Fatalf("mkdir g: %v", err)
			}
			gid := db.UUIDString(g.ID)
			p, err := fs.CreateFolder(ctx, userID, &gid, "p")
			if err != nil {
				t.Fatalf("mkdir p: %v", err)
			}
			pid := db.UUIDString(p.ID)
			if _, err := fs.Push(ctx, userID, &pid, "x.txt", nil, "i", strings.NewReader("v0")); err != nil {
				t.Fatalf("push: %v", err)
			}
			target := pid
			if renamed == "grandparent" {
				target = gid
			}
			for i := 1; i <= 15; i++ {
				content := fmt.Sprintf("v%d", i)
				errPush, errRename := race(
					func() error {
						_, err := fs.Push(ctx, userID, &pid, "x.txt", nil, "i", strings.NewReader(content))
						return err
					},
					func() error {
						_, err := fs.Rename(ctx, userID, target, fmt.Sprintf("%s%d", renamed, i))
						return err
					},
				)
				if errPush != nil || errRename != nil {
					t.Fatalf("round %d: push=%v rename=%v", i, errPush, errRename)
				}
				assertTreeConsistent(t, pool, userID, root)
				x, err := pool.Query(ctx, `SELECT disk_path FROM nodes WHERE parent_id = $1::uuid AND name = 'x.txt' AND deleted_at IS NULL`, pid)
				if err != nil {
					t.Fatalf("x: %v", err)
				}
				var xp string
				for x.Next() {
					_ = x.Scan(&xp)
				}
				x.Close()
				b, err := os.ReadFile(filepath.Join(root, xp))
				if err != nil || string(b) != content {
					t.Fatalf("round %d: x.txt at %s = %q, %v; want %q", i, xp, b, err, content)
				}
			}
		})
	}
}

// A move of a into its sibling b races a delete of their parent. The delete must
// succeed; the move either lands first (and is trashed with the rest) or finds its
// node gone. Either way nothing live is left under a trashed folder.
func TestMoveIntoSiblingRacingParentDelete(t *testing.T) {
	ctx := context.Background()
	fs, _, pool, userID, root := setupFSPool(t, nil)
	for i := 1; i <= 10; i++ {
		P, err := fs.CreateFolder(ctx, userID, nil, fmt.Sprintf("P%d", i))
		if err != nil {
			t.Fatalf("mkdir P: %v", err)
		}
		pid := db.UUIDString(P.ID)
		a, err := fs.CreateFolder(ctx, userID, &pid, "a")
		if err != nil {
			t.Fatalf("mkdir a: %v", err)
		}
		aid := db.UUIDString(a.ID)
		if _, err := fs.Push(ctx, userID, &aid, "f.txt", nil, "i", strings.NewReader("f")); err != nil {
			t.Fatalf("push: %v", err)
		}
		b, err := fs.CreateFolder(ctx, userID, &pid, "b")
		if err != nil {
			t.Fatalf("mkdir b: %v", err)
		}
		bid := db.UUIDString(b.ID)
		errMove, errDelete := race(
			func() error { _, err := fs.Move(ctx, userID, aid, &bid); return err },
			func() error { return fs.Delete(ctx, userID, pid) },
		)
		if errDelete != nil {
			t.Fatalf("round %d: delete: %v", i, errDelete)
		}
		if errMove != nil && !errors.Is(errMove, storage.ErrNotFound) {
			t.Fatalf("round %d: move: %v", i, errMove)
		}
		assertTreeConsistent(t, pool, userID, root)
		var live int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE user_id = $1::uuid AND deleted_at IS NULL AND disk_path LIKE $2`,
			userID, P.DiskPath.String+"/%").Scan(&live); err != nil || live != 0 {
			t.Fatalf("round %d: %d live node(s) left under the deleted folder (%v)", i, live, err)
		}
	}
}

// A restore whose parent is trashed while the restore runs goes to the root, as a
// restore under an already trashed parent does, instead of failing.
func TestUndeleteFallsBackToRootWhenParentTrashedMeanwhile(t *testing.T) {
	ctx := context.Background()
	fs, q, pool, userID, _ := setupFSPool(t, nil)
	uid, _ := db.ParseUUID(userID)
	p, err := fs.CreateFolder(ctx, userID, nil, "p")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pid := db.UUIDString(p.ID)
	c, err := fs.Push(ctx, userID, &pid, "c.txt", nil, "i", strings.NewReader("c"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	cid := db.UUIDString(c.Node.ID)
	if err := fs.Delete(ctx, userID, cid); err != nil {
		t.Fatalf("delete c: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	qtx := q.WithTx(tx)
	// What Delete(p) does before its commit.
	if _, err := qtx.LockLiveSubtreeDirs(ctx, db.LockLiveSubtreeDirsParams{UserID: uid, Prefix: p.DiskPath.String}); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if err := qtx.SoftDeleteSubtree(ctx, db.SoftDeleteSubtreeParams{UserID: uid, Prefix: p.DiskPath.String}); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	type result struct {
		n   db.Node
		err error
	}
	done := make(chan result, 1)
	go func() { n, err := fs.Undelete(ctx, userID, cid); done <- result{n, err} }()
	errc := make(chan error, 1)
	go func() {
		r := <-done
		done <- r
		errc <- r.err
	}()
	waitForLockWait(t, pool, errc)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("undelete: %v, want a restore to the root", r.err)
	}
	if r.n.ParentID.Valid || r.n.DiskPath.String != userID+"/c.txt" {
		t.Fatalf("restored to %s (parent %v), want the root", r.n.DiskPath.String, r.n.ParentID.Valid)
	}
}

// A folder d is created under P/p while Delete(P) or Rename(P) waits for that create to
// commit. d is not in the snapshot of the statement that was waiting, so unless the
// subtree lock is taken again d stays unlocked, and a push into d that commits after the
// delete's / rename's own snapshot leaves a live file under a trashed folder, or at a
// path the rename already moved away. A statement trigger widens that window.
func TestSubtreeLockCoversFoldersCreatedWhileWaiting(t *testing.T) {
	for _, op := range []string{"delete", "rename"} {
		t.Run(op, func(t *testing.T) {
			ctx := context.Background()
			fs, q, pool, userID, root := setupFSPool(t, nil)
			uid, _ := db.ParseUUID(userID)
			P, err := fs.CreateFolder(ctx, userID, nil, "P")
			if err != nil {
				t.Fatalf("mkdir P: %v", err)
			}
			Pid := db.UUIDString(P.ID)
			p, err := fs.CreateFolder(ctx, userID, &Pid, "p")
			if err != nil {
				t.Fatalf("mkdir p: %v", err)
			}
			// Once armed, the next UPDATE statement on nodes (the soft delete or the path
			// rewrite) sleeps after taking its snapshot.
			if _, err := pool.Exec(ctx, `
				CREATE TABLE test_pause (x int);
				CREATE FUNCTION test_pause() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN
					IF EXISTS (SELECT 1 FROM test_pause) THEN DELETE FROM test_pause; PERFORM pg_sleep(1.5); END IF;
					RETURN NULL;
				END $$;
				CREATE TRIGGER test_pause BEFORE UPDATE ON nodes FOR EACH STATEMENT EXECUTE FUNCTION test_pause();`); err != nil {
				t.Fatalf("trigger: %v", err)
			}

			// CreateFolder(d under p), held open after its parent lock and insert.
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer tx.Rollback(ctx)
			qtx := q.WithTx(tx)
			if _, err := qtx.LockLiveParent(ctx, p.ID); err != nil {
				t.Fatalf("lock parent: %v", err)
			}
			drel := p.DiskPath.String + "/d"
			d, err := qtx.CreateNode(ctx, db.CreateNodeParams{
				UserID: uid, ParentID: p.ID, Name: "d", IsDir: true, DiskPath: pgtype.Text{String: drel, Valid: true},
			})
			if err != nil {
				t.Fatalf("create d: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(root, drel), 0o755); err != nil {
				t.Fatalf("mkdir d on disk: %v", err)
			}

			done := make(chan error, 1)
			go func() {
				if op == "delete" {
					done <- fs.Delete(ctx, userID, Pid)
				} else {
					_, err := fs.Rename(ctx, userID, Pid, "P2")
					done <- err
				}
			}()
			waitForLockWait(t, pool, done)
			if _, err := pool.Exec(ctx, `INSERT INTO test_pause VALUES (1)`); err != nil {
				t.Fatalf("arm: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("commit d: %v", err)
			}
			time.Sleep(300 * time.Millisecond) // the operation is now inside its paused statement
			did := db.UUIDString(d.ID)
			_, errPush := fs.Push(ctx, userID, &did, "late.txt", nil, "i", strings.NewReader("l"))
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", op, err)
			}

			assertTreeConsistent(t, pool, userID, root)
			switch op {
			case "delete":
				// The push waited for the delete and found d trashed.
				if !errors.Is(errPush, storage.ErrNotFound) {
					t.Fatalf("push into d during the delete = %v, want ErrNotFound", errPush)
				}
			case "rename":
				// The push waited for the rename and then wrote under the new path.
				if errPush != nil {
					t.Fatalf("push into d during the rename: %v", errPush)
				}
				if _, err := os.Stat(filepath.Join(root, userID, "P2", "p", "d", "late.txt")); err != nil {
					t.Fatalf("pushed file not under the renamed folder: %v", err)
				}
			}
		})
	}
}
