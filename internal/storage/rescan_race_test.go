package storage_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"discodrive/internal/storage"
)

// pausingDisk parks the first Move whose destination ends with target, after the move
// is done on disk: the operation's database transaction is still open at that point.
// With onRead set it instead parks inside Walk, after Rescan has read the database.
type pausingDisk struct {
	storage.Storage
	target  string
	onRead  bool
	paused  chan struct{}
	release chan struct{}
}

func newPausingDisk(target string, onRead bool) *pausingDisk {
	return &pausingDisk{target: target, onRead: onRead, paused: make(chan struct{}), release: make(chan struct{})}
}

func (p *pausingDisk) park() {
	select {
	case <-p.paused:
		return // only the first hit parks
	default:
	}
	close(p.paused)
	<-p.release
}

func (p *pausingDisk) Move(oldRel, newRel string) error {
	err := p.Storage.Move(oldRel, newRel)
	if !p.onRead && p.target != "" && strings.HasSuffix(newRel, p.target) {
		p.park()
	}
	return err
}

func (p *pausingDisk) ReadDir(rel string) ([]storage.DirItem, error) {
	if p.onRead {
		p.park()
	}
	return p.Storage.ReadDir(rel)
}

// rescanDuring runs Rescan while op is parked and fails if Rescan does not come back
// on its own (it used to wait on the operation's uncommitted rows).
func rescanDuring(t *testing.T, fs *storage.FileService, disk *pausingDisk) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fs.Rescan(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			close(disk.release)
			t.Fatalf("rescan during the operation: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(disk.release)
		t.Fatal("rescan raced the operation: it blocked on rows the operation had not committed")
	}
}

func liveNames(t *testing.T, fs *storage.FileService, userID string) map[string]string {
	t.Helper()
	nodes, err := fs.RootChildren(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, n := range nodes {
		if _, dup := out[n.Name]; dup {
			t.Fatalf("two live nodes named %q", n.Name)
		}
		out[n.Name] = n.ID.String()
	}
	return out
}

// An upload is on disk before its node is committed. The rescan must leave that file to
// the upload instead of hashing and importing it.
func TestRescanLeavesUploadInFlightAlone(t *testing.T) {
	var disk *pausingDisk
	fs, _, userID, _ := setupFSWith(t, func(st storage.Storage) storage.Storage {
		disk = newPausingDisk("/a.txt", false)
		disk.Storage = st
		return disk
	})
	pushed := make(chan error, 1)
	go func() {
		_, err := fs.Push(context.Background(), userID, nil, "a.txt", nil, "", strings.NewReader("hello"))
		pushed <- err
	}()
	<-disk.paused
	rescanDuring(t, fs, disk)
	close(disk.release)
	if err := <-pushed; err != nil {
		t.Fatalf("push: %v", err)
	}
	if names := liveNames(t, fs, userID); len(names) != 1 {
		t.Fatalf("live nodes %v, want just a.txt", names)
	}
}

// A rename moves the file on disk before the new path is committed. The rescan must not
// take the old node for missing (soft-deleting it, with its shares and versions) nor
// import the new path as a second file.
func TestRescanLeavesRenameInFlightAlone(t *testing.T) {
	var disk *pausingDisk
	fs, _, userID, _ := setupFSWith(t, func(st storage.Storage) storage.Storage {
		disk = newPausingDisk("/new.txt", false)
		disk.Storage = st
		return disk
	})
	res, err := fs.Push(context.Background(), userID, nil, "old.txt", nil, "", strings.NewReader("keep me"))
	if err != nil {
		t.Fatal(err)
	}
	renamed := make(chan error, 1)
	go func() {
		_, err := fs.Rename(context.Background(), userID, res.Node.ID.String(), "new.txt")
		renamed <- err
	}()
	<-disk.paused
	rescanDuring(t, fs, disk)
	close(disk.release)
	if err := <-renamed; err != nil {
		t.Fatalf("rename: %v", err)
	}
	names := liveNames(t, fs, userID)
	if len(names) != 1 || names["new.txt"] != res.Node.ID.String() {
		t.Fatalf("live nodes %v, want only the original node as new.txt", names)
	}
}

// A rename that completes between the rescan's database read and its disk walk leaves
// the rescan with a stale view; it must re-check before acting on it.
func TestRescanRechecksAfterStaleSnapshot(t *testing.T) {
	var disk *pausingDisk
	fs, _, userID, _ := setupFSWith(t, func(st storage.Storage) storage.Storage {
		disk = newPausingDisk("", true)
		disk.Storage = st
		return disk
	})
	res, err := fs.Push(context.Background(), userID, nil, "old.txt", nil, "", strings.NewReader("keep me"))
	if err != nil {
		t.Fatal(err)
	}
	scanned := make(chan error, 1)
	go func() { scanned <- fs.Rescan(context.Background()) }()
	<-disk.paused // Rescan has listed the nodes: old.txt
	if _, err := fs.Rename(context.Background(), userID, res.Node.ID.String(), "new.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	close(disk.release) // the walk now sees new.txt only
	if err := <-scanned; err != nil {
		t.Fatalf("rescan: %v", err)
	}
	names := liveNames(t, fs, userID)
	if len(names) != 1 || names["new.txt"] != res.Node.ID.String() {
		t.Fatalf("live nodes %v, want only the original node as new.txt", names)
	}
}
