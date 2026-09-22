package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
	"discodrive/internal/quota"
)

var (
	ErrInvalidName = errors.New("invalid name")
	ErrNotFound    = errors.New("not found")
	ErrNotDir      = errors.New("parent is not a directory")
	ErrNameTaken   = errors.New("name already taken in this folder")
	ErrCycle       = errors.New("cannot move a folder into itself")
)

// FileService links the node tree in the database with its on-disk mirror (Storage).
type FileService struct {
	pool interface {
		Begin(context.Context) (pgx.Tx, error)
	}
	q  *db.Queries
	st Storage
	// quota bounds what a user may write; nil = no limits configured (tests, and
	// deployments that set neither user quotas nor a server-wide cap).
	quota *quota.Checker
	// noVersions turns off version history entirely (VERSION_KEEP=0): an overwrite
	// then replaces the content and nothing is kept.
	noVersions bool

	// rescanMu serializes Rescan: the periodic ticker and the fsnotify watcher
	// may fire together, and two concurrent walks would race to insert the same
	// discovered nodes.
	rescanMu sync.Mutex
}

func NewFileService(pool *pgxpool.Pool, st Storage) *FileService {
	return &FileService{pool: pool, q: db.New(pool), st: st}
}

// SetQuota installs the quota checker. Called once at startup, before the service
// handles requests.
func (s *FileService) SetQuota(c *quota.Checker) { s.quota = c }

// DisableVersions turns off version snapshots (VERSION_KEEP=0). Overwrites then simply
// replace the file: no history, no rollback, and no disk beyond the files themselves.
// Called once at startup; version history is on by default.
func (s *FileService) DisableVersions() { s.noVersions = true }

// Quota returns the checker enforcing writes; nil when no limits are configured (or
// when there is no file service at all, which some tests construct).
func (s *FileService) Quota() *quota.Checker {
	if s == nil {
		return nil
	}
	return s.quota
}

// CheckQuota reports whether userID may write n more bytes; ErrExceeded if not.
// Callers that know the size up front (a declared Content-Length) use it to refuse
// before the transfer instead of during it.
func (s *FileService) CheckQuota(ctx context.Context, userID string, n int64) error {
	if s.quota == nil {
		return nil
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return ErrNotFound
	}
	return s.quota.Check(ctx, uid, n)
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return ErrInvalidName
	}
	return nil
}

// canAccess is the central access check. The node owner has full access;
// otherwise we look for an active share on the node OR any ancestor (downward inheritance).
func (s *FileService) canAccess(ctx context.Context, authedUser string, node db.Node, needWrite bool) (bool, error) {
	if db.UUIDString(node.UserID) == authedUser {
		return true, nil
	}
	uid, err := db.ParseUUID(authedUser)
	if err != nil {
		return false, nil
	}
	acc, err := s.q.SharedAccessForUser(ctx, db.SharedAccessForUserParams{StartID: node.ID, UserID: uid})
	if err != nil {
		return false, err
	}
	if needWrite {
		return acc.CanWrite, nil
	}
	return acc.CanRead, nil
}

// accessNode loads a node by ID and checks access; no access → ErrNotFound
// (we do not reveal the existence of nodes owned by others).
func (s *FileService) accessNode(ctx context.Context, authedUser, nodeID string, needWrite bool) (db.Node, error) {
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return db.Node{}, ErrNotFound
	}
	node, err := s.q.GetNode(ctx, nid)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Node{}, ErrNotFound
	}
	if err != nil {
		return db.Node{}, err
	}
	ok, err := s.canAccess(ctx, authedUser, node, needWrite)
	if err != nil {
		return db.Node{}, err
	}
	if !ok {
		return db.Node{}, ErrNotFound
	}
	return node, nil
}

// resolveParent returns the relPath prefix for the parent, its UUID, and the owner UUID
// (nodes inside a shared folder belong to the tree owner, not the uploading user).
// parentID == nil → the user's own root. Writing into another user's folder requires permission.
func (s *FileService) resolveParent(ctx context.Context, authedUser string, parentID *string) (prefix string, parentUUID, ownerUUID pgtype.UUID, err error) {
	auid, err := db.ParseUUID(authedUser)
	if err != nil {
		return "", pgtype.UUID{}, pgtype.UUID{}, ErrNotFound
	}
	if parentID == nil || *parentID == "" {
		return authedUser, pgtype.UUID{}, auid, nil
	}
	parent, err := s.ownerNode(ctx, authedUser, *parentID)
	if err != nil {
		return "", pgtype.UUID{}, pgtype.UUID{}, err
	}
	if !parent.IsDir {
		return "", pgtype.UUID{}, pgtype.UUID{}, ErrNotDir
	}
	return parent.DiskPath.String, parent.ID, parent.UserID, nil
}

// CreateFolder creates a directory: a row in nodes and a directory on disk.
func (s *FileService) CreateFolder(ctx context.Context, userID string, parentID *string, name string) (db.Node, error) {
	if err := validateName(name); err != nil {
		return db.Node{}, err
	}
	prefix, parentUUID, ownerUUID, err := s.resolveParent(ctx, userID, parentID)
	if err != nil {
		return db.Node{}, err
	}
	rel := prefix + "/" + name

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Node{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	node, err := qtx.CreateNode(ctx, db.CreateNodeParams{
		UserID:   ownerUUID,
		ParentID: parentUUID,
		Name:     name,
		IsDir:    true,
		DiskPath: text(rel),
	})
	if err != nil {
		return db.Node{}, mapInsertErr(err)
	}
	if err := s.st.Mkdir(rel); err != nil {
		return db.Node{}, err
	}
	if err := recordChange(ctx, qtx, ownerUUID, node.ID, "create", node.Version); err != nil {
		return db.Node{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Node{}, err
	}
	return node, nil
}

// UploadFile handles a web upload (no base_version, last-write semantics). Delegates to Push.
func (s *FileService) UploadFile(ctx context.Context, userID string, parentID *string, name string, r io.Reader) (db.Node, error) {
	res, err := s.Push(ctx, userID, parentID, name, nil, "", r)
	if err != nil {
		return db.Node{}, err
	}
	return res.Node, nil
}

// Rename renames a node: updates disk_path for the subtree in the DB and on disk.
func (s *FileService) Rename(ctx context.Context, userID, nodeID, newName string) (db.Node, error) {
	if err := validateName(newName); err != nil {
		return db.Node{}, err
	}
	node, err := s.ownerNode(ctx, userID, nodeID)
	if err != nil {
		return db.Node{}, err
	}
	owner := node.UserID
	oldRel := node.DiskPath.String
	newRel := parentDir(oldRel) + "/" + newName

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Node{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	if err := qtx.RewriteSubtreePaths(ctx, db.RewriteSubtreePathsParams{UserID: owner, OldPrefix: oldRel, NewPrefix: newRel}); err != nil {
		return db.Node{}, err
	}
	updated, err := qtx.UpdateNodeName(ctx, db.UpdateNodeNameParams{ID: node.ID, Name: newName})
	if err != nil {
		return db.Node{}, mapInsertErr(err)
	}
	if err := s.st.Move(oldRel, newRel); err != nil {
		return db.Node{}, err
	}
	if err := recordChange(ctx, qtx, owner, node.ID, "update", updated.Version); err != nil {
		return db.Node{}, err
	}
	if err := qtx.RecordSubtreeChanges(ctx, db.RecordSubtreeChangesParams{UserID: owner, Prefix: newRel}); err != nil {
		return db.Node{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Node{}, err
	}
	return updated, nil
}

// Move reparents a node (parentID == nil → root).
func (s *FileService) Move(ctx context.Context, userID, nodeID string, parentID *string) (db.Node, error) {
	node, err := s.ownerNode(ctx, userID, nodeID)
	if err != nil {
		return db.Node{}, err
	}
	prefix, parentUUID, ownerUUID, err := s.resolveParent(ctx, userID, parentID)
	if err != nil {
		return db.Node{}, err
	}
	// moves are only allowed within the same owner's tree (cross-owner moves deferred to 0.8)
	if db.UUIDString(ownerUUID) != db.UUIDString(node.UserID) {
		return db.Node{}, ErrNotFound
	}
	owner := node.UserID
	oldRel := node.DiskPath.String
	// prevent moving a folder into itself or one of its descendants
	if prefix == oldRel || strings.HasPrefix(prefix, oldRel+"/") {
		return db.Node{}, ErrCycle
	}
	newRel := prefix + "/" + node.Name

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Node{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	if err := qtx.RewriteSubtreePaths(ctx, db.RewriteSubtreePathsParams{UserID: owner, OldPrefix: oldRel, NewPrefix: newRel}); err != nil {
		return db.Node{}, err
	}
	updated, err := qtx.UpdateNodeParent(ctx, db.UpdateNodeParentParams{ID: node.ID, ParentID: parentUUID})
	if err != nil {
		return db.Node{}, mapInsertErr(err)
	}
	if err := s.st.Move(oldRel, newRel); err != nil {
		return db.Node{}, err
	}
	if err := recordChange(ctx, qtx, owner, node.ID, "move", updated.Version); err != nil {
		return db.Node{}, err
	}
	if err := qtx.RecordSubtreeChanges(ctx, db.RecordSubtreeChangesParams{UserID: owner, Prefix: newRel}); err != nil {
		return db.Node{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Node{}, err
	}
	return updated, nil
}

// Delete soft-deletes a node and its subtree (disk cleanup is done by GC, step 0.6).
// The tombstone of the top node is written to change_log — the client deletes the entire subtree locally.
func (s *FileService) Delete(ctx context.Context, userID, nodeID string) error {
	node, err := s.ownerNode(ctx, userID, nodeID)
	if err != nil {
		return err
	}
	owner := node.UserID
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	if err := qtx.SoftDeleteSubtree(ctx, db.SoftDeleteSubtreeParams{UserID: owner, Prefix: node.DiskPath.String}); err != nil {
		return err
	}
	ver, err := qtx.BumpNodeVersion(ctx, node.ID)
	if err != nil {
		return err
	}
	if err := recordChange(ctx, qtx, owner, node.ID, "delete", ver); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Restore rolls back a file's content to the given version, creating a new version.
func (s *FileService) Restore(ctx context.Context, userID, nodeID string, version int64) (db.Node, error) {
	node, err := s.ownerNode(ctx, userID, nodeID)
	if err != nil {
		return db.Node{}, err
	}
	if node.IsDir {
		return db.Node{}, ErrNotFound
	}
	owner := node.UserID
	fv, err := s.q.GetFileVersion(ctx, db.GetFileVersionParams{NodeID: node.ID, Version: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Node{}, ErrNotFound
	}
	if err != nil {
		return db.Node{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Node{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	// Preserve the content being rolled back, so the rollback itself can be undone.
	if err := s.snapshot(ctx, qtx, node); err != nil {
		return db.Node{}, err
	}
	// make the target version's content the current content
	if err := s.st.Copy(fv.DiskPath.String, node.DiskPath.String); err != nil {
		return db.Node{}, err
	}
	updated, err := qtx.UpdateNodeContent(ctx, db.UpdateNodeContentParams{
		ID: node.ID, Size: fv.Size, ContentHash: fv.ContentHash, Mime: node.Mime,
	})
	if err != nil {
		return db.Node{}, err
	}
	if err := recordChange(ctx, qtx, owner, node.ID, "update", updated.Version); err != nil {
		return db.Node{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Node{}, err
	}
	return updated, nil
}

// ListVersions returns the version history of a file (access checked via canAccess).
func (s *FileService) ListVersions(ctx context.Context, userID, nodeID string) ([]db.FileVersion, error) {
	node, err := s.accessNode(ctx, userID, nodeID, false)
	if err != nil {
		return nil, err
	}
	return s.q.ListFileVersions(ctx, node.ID)
}

// NodeForDownload returns a file node for downloading (access checked via canAccess),
// without opening the file — bytes are served by nginx via X-Accel-Redirect.
func (s *FileService) NodeForDownload(ctx context.Context, userID, nodeID string) (db.Node, error) {
	node, err := s.accessNode(ctx, userID, nodeID, false)
	if err != nil {
		return db.Node{}, err
	}
	if node.IsDir {
		return db.Node{}, ErrNotFound
	}
	return node, nil
}

// Open returns a node and an open file handle for downloading (access checked via canAccess).
func (s *FileService) Open(ctx context.Context, userID, nodeID string) (db.Node, *os.File, error) {
	node, err := s.accessNode(ctx, userID, nodeID, false)
	if err != nil {
		return db.Node{}, nil, err
	}
	if node.IsDir {
		return db.Node{}, nil, ErrNotFound
	}
	f, err := s.st.Open(node.DiskPath.String)
	if err != nil {
		return db.Node{}, nil, err
	}
	return node, f, nil
}

// ListChildren returns the contents of a directory (read permission required on the folder);
// children belong to the tree owner, not the requesting user.
func (s *FileService) ListChildren(ctx context.Context, userID, nodeID string) ([]db.Node, error) {
	node, err := s.accessNode(ctx, userID, nodeID, false)
	if err != nil {
		return nil, err
	}
	if !node.IsDir {
		return nil, ErrNotDir
	}
	return s.q.ListNodeChildren(ctx, node.ID)
}

// NodeByPath resolves a DAV path ("/a/b") to a node in the user's own tree.
// disk_path mirrors the tree with the owner UUID prefix: "<uuid>/a/b".
func (s *FileService) NodeByPath(ctx context.Context, userID, davPath string) (db.Node, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Node{}, ErrNotFound
	}
	rel := path.Join(userID, strings.TrimPrefix(path.Clean("/"+davPath), "/"))
	node, err := s.q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: uid, Path: rel})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Node{}, ErrNotFound
	}
	return node, err
}

// RootChildren returns the nodes at the root of the user's tree (parent_id IS NULL).
func (s *FileService) RootChildren(ctx context.Context, userID string) ([]db.Node, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.q.ListRootNodes(ctx, uid)
}

// PushResult is the outcome of Push: the resulting node and whether a conflict occurred.
type PushResult struct {
	Node       db.Node
	Conflicted bool
}

// Push is the entry point for the sync protocol: the client sends content and
// base_version (the version it was working from). The server compares it to the current
// node version. Match / new file → accept. Mismatch → conflict: the server version stays
// as the primary, the client version is saved as a separate conflict copy.
//
// baseVersion == nil → no version check (plain web upload, last-write semantics).
func (s *FileService) Push(ctx context.Context, userID string, parentID *string, name string, baseVersion *int64, device string, r io.Reader) (PushResult, error) {
	return s.PushWithMeta(ctx, userID, parentID, name, baseVersion, device, r, PushMeta{})
}

// PushMeta carries optional client-supplied metadata. The zero value means "the client
// told us nothing", which is exactly the behaviour Push has always had.
type PushMeta struct {
	// ModifiedAt is when the content itself last changed on the client. Zero leaves the
	// node dated with server time, i.e. when it was uploaded. Version snapshots keep
	// server time either way: they record when a revision reached us, not when it was
	// authored, and that ordering is what history browsing relies on.
	ModifiedAt time.Time
}

// PushWithMeta is Push plus client-supplied metadata; see Push for the sync semantics.
func (s *FileService) PushWithMeta(ctx context.Context, userID string, parentID *string, name string, baseVersion *int64, device string, r io.Reader, meta PushMeta) (PushResult, error) {
	if err := validateName(name); err != nil {
		return PushResult{}, err
	}
	prefix, parentUUID, ownerUUID, err := s.resolveParent(ctx, userID, parentID)
	if err != nil {
		return PushResult{}, err
	}
	rel := prefix + "/" + name

	staged, err := s.stage(ctx, ownerUUID, r)
	if err != nil {
		return PushResult{}, err
	}
	defer staged.cleanup(s.st)
	tmpRel, size, sha := staged.rel, staged.size, staged.hash
	q, begin := s.q, s.pool.Begin
	if staged.reservation != nil {
		q = staged.reservation.Queries()
		begin = staged.reservation.Connection().Begin
	}
	tx, err := begin(ctx)
	if err != nil {
		return PushResult{}, err
	}
	defer tx.Rollback(ctx)
	qtx := q.WithTx(tx)

	existing, err := qtx.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: ownerUUID, Path: rel})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// new file
		if err := s.st.Move(tmpRel, rel); err != nil {
			return PushResult{}, err
		}
		node, err := qtx.CreateNode(ctx, db.CreateNodeParams{
			UserID: ownerUUID, ParentID: parentUUID, Name: name, IsDir: false,
			Size: int8val(size), ContentHash: text(sha), DiskPath: text(rel), Mime: text(detectMime(name)),
			ModifiedAt: tsval(meta.ModifiedAt),
		})
		if err != nil {
			return PushResult{}, mapInsertErr(err)
		}
		return s.finishPush(ctx, tx, qtx, ownerUUID, node, "create", false)

	case err != nil:
		return PushResult{}, err

	case existing.IsDir:
		return PushResult{}, ErrNameTaken
	}

	// File exists. base_version matches (or was not provided) → accept.
	if baseVersion == nil || *baseVersion == existing.Version {
		// Preserve the content about to be overwritten, under the version it belongs to.
		// The newest version is always the live file itself and is never copied into
		// .versions — snapshotting it too would store every file twice.
		if err := s.snapshot(ctx, qtx, existing); err != nil {
			return PushResult{}, err
		}
		if err := s.st.Move(tmpRel, rel); err != nil { // overwrites the primary file
			return PushResult{}, err
		}
		node, err := qtx.UpdateNodeContent(ctx, db.UpdateNodeContentParams{
			ID: existing.ID, Size: int8val(size), ContentHash: text(sha), Mime: text(detectMime(name)),
			ModifiedAt: tsval(meta.ModifiedAt),
		})
		if err != nil {
			return PushResult{}, err
		}
		return s.finishPush(ctx, tx, qtx, ownerUUID, node, "update", false)
	}

	// CONFLICT: the server version stays as the primary; the client version becomes
	// a separate copy named `name (conflict, device, date).ext`.
	cname := conflictName(name, device, time.Now())
	crel := prefix + "/" + cname
	if err := s.st.Move(tmpRel, crel); err != nil {
		return PushResult{}, err
	}
	cnode, err := qtx.CreateConflictNode(ctx, db.CreateConflictNodeParams{
		UserID: ownerUUID, ParentID: parentUUID, Name: cname,
		Size: int8val(size), ContentHash: text(sha), DiskPath: text(crel),
		Mime: text(detectMime(name)), ConflictOf: existing.ID,
		ModifiedAt: tsval(meta.ModifiedAt), // the conflict copy is the client's file, so it keeps the client's date
	})
	if err != nil {
		return PushResult{}, mapInsertErr(err)
	}
	return s.finishPush(ctx, tx, qtx, ownerUUID, cnode, "create", true)
}

// finishPush appends to change_log and commits the transaction. It does not snapshot:
// the content it publishes IS the newest version, and that version lives in the file
// itself. Only content a push replaces goes to .versions.
func (s *FileService) finishPush(ctx context.Context, tx pgx.Tx, qtx *db.Queries, uid pgtype.UUID, node db.Node, op string, conflicted bool) (PushResult, error) {
	if err := recordChange(ctx, qtx, uid, node.ID, op, node.Version); err != nil {
		return PushResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PushResult{}, err
	}
	return PushResult{Node: node, Conflicted: conflicted}, nil
}

// ReplaceContentInPlace overwrites the content of an existing file node without
// snapshotting a new version. It updates size/hash/mime and records a sync
// "update" change. Used by the tag editor's no-version mode.
func (s *FileService) ReplaceContentInPlace(ctx context.Context, userID, nodeID string, r io.Reader) (db.Node, error) {
	node, err := s.ownerNode(ctx, userID, nodeID)
	if err != nil {
		return db.Node{}, err
	}
	if node.IsDir {
		return db.Node{}, ErrNameTaken
	}

	staged, err := s.stage(ctx, node.UserID, r)
	if err != nil {
		return db.Node{}, err
	}
	defer staged.cleanup(s.st)
	tmpRel, size, sha := staged.rel, staged.size, staged.hash
	q, begin := s.q, s.pool.Begin
	if staged.reservation != nil {
		q = staged.reservation.Queries()
		begin = staged.reservation.Connection().Begin
	}
	tx, err := begin(ctx)
	if err != nil {
		return db.Node{}, err
	}
	defer tx.Rollback(ctx)
	qtx := q.WithTx(tx)

	if err := s.st.Move(tmpRel, node.DiskPath.String); err != nil {
		return db.Node{}, err
	}
	updated, err := qtx.UpdateNodeContent(ctx, db.UpdateNodeContentParams{
		ID: node.ID, Size: int8val(size), ContentHash: text(sha), Mime: text(detectMime(node.Name)),
	})
	if err != nil {
		return db.Node{}, err
	}
	if err := recordChange(ctx, qtx, node.UserID, updated.ID, "update", updated.Version); err != nil {
		return db.Node{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Node{}, err
	}
	return updated, nil
}

// recordChange allocates a monotonic seq for the user and appends a row to change_log.
func recordChange(ctx context.Context, qtx *db.Queries, userID, nodeID pgtype.UUID, op string, version int64) error {
	seq, err := qtx.NextChangeSeq(ctx, userID)
	if err != nil {
		return err
	}
	_, err = qtx.AppendChange(ctx, db.AppendChangeParams{
		UserID: userID, NodeID: nodeID, Seq: seq, Op: op, Version: version,
	})
	return err
}

// snapshot copies the content a write is about to replace into the version store and
// writes a file_versions row for the version it belonged to (trimmed to VERSION_KEEP by
// the GC job). Callers pass the node as it is BEFORE the new content lands.
//
// Snapshots are a copy of the whole file, so a file that is written once and never
// touched again costs exactly its own size; only editing it starts a history.
func (s *FileService) snapshot(ctx context.Context, qtx *db.Queries, node db.Node) error {
	if s.noVersions {
		return nil
	}
	vpath := versionPath(db.UUIDString(node.UserID), db.UUIDString(node.ID), node.Version)
	if err := s.st.Copy(node.DiskPath.String, vpath); err != nil {
		return err
	}
	return qtx.InsertFileVersion(ctx, db.InsertFileVersionParams{
		NodeID:      node.ID,
		Version:     node.Version,
		ContentHash: node.ContentHash,
		DiskPath:    text(vpath),
		Size:        node.Size,
	})
}

// versionPath returns the snapshot path outside the tree mirror (ignored by rescan in 0.6).
func versionPath(userID, nodeID string, version int64) string {
	return ".versions/" + userID + "/" + nodeID + "/" + strconv.FormatInt(version, 10)
}

// --- Background jobs (step 0.6) ---

// TrimVersions deletes versions beyond the keep newest for each file (disk + DB).
// Runs asynchronously, not inline during upload.
func (s *FileService) TrimVersions(ctx context.Context, keep int) error {
	nodeIDs, err := s.q.ListNodesWithExcessVersions(ctx, int32(keep))
	if err != nil {
		return err
	}
	for _, nid := range nodeIDs {
		paths, err := s.q.TrimNodeVersions(ctx, db.TrimNodeVersionsParams{Nid: nid, Keep: int32(keep)})
		if err != nil {
			return err
		}
		for _, p := range paths {
			if p.Valid {
				_ = s.st.Remove(p.String)
			}
		}
	}
	return nil
}

// PruneLiveSnapshots removes version snapshots that duplicate a file's current content.
// They are leftovers from the scheme where a push snapshotted what it had just written,
// so every stored file also sat in .versions — this is what gives that space back.
// Nodes modified within idleFor are skipped: their newest snapshot may be a push still
// in flight, whose row is not committed yet.
func (s *FileService) PruneLiveSnapshots(ctx context.Context, idleFor time.Duration) error {
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-idleFor), Valid: true}
	rows, err := s.q.ListRedundantVersionSnapshots(ctx, cutoff)
	if err != nil {
		return err
	}
	for _, r := range rows {
		// Row first: an orphaned file is cleaned up by the next run of this job (and by
		// TrashGC), while an orphaned row would offer a rollback to a missing file.
		if err := s.q.DeleteFileVersion(ctx, r.ID); err != nil {
			return err
		}
		if r.DiskPath.Valid {
			_ = s.st.Remove(r.DiskPath.String)
		}
	}
	if len(rows) > 0 {
		log.Printf("discodrive: pruned %d version snapshot(s) duplicating live files", len(rows))
	}
	return nil
}

// TrashGC physically removes nodes that have been tombstoned for longer than olderThan:
// the file/folder from disk, version snapshots, and the nodes row (change_log/file_versions cascade).
func (s *FileService) TrashGC(ctx context.Context, olderThan time.Duration) error {
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-olderThan), Valid: true}
	rows, err := s.q.ListExpiredTombstones(ctx, cutoff)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.DiskPath.Valid {
			_ = s.st.Remove(r.DiskPath.String)
		}
		_ = s.st.Remove(versionDir(db.UUIDString(r.UserID), db.UUIDString(r.ID)))
		if err := s.q.HardDeleteNode(ctx, r.ID); err != nil {
			return err
		}
	}
	return nil
}

// Rescan reconciles disk and DB for all users: files added outside the service are
// imported into nodes+change_log; missing nodes are soft-deleted. One user's
// failure does not block the others; all errors are joined into the result.
func (s *FileService) Rescan(ctx context.Context) error {
	s.rescanMu.Lock()
	defer s.rescanMu.Unlock()

	users, err := s.q.ListUserIDs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, uid := range users {
		if err := s.rescanUser(ctx, uid); err != nil {
			errs = append(errs, fmt.Errorf("user %s: %w", db.UUIDString(uid), err))
		}
	}
	return errors.Join(errs...)
}

func (s *FileService) rescanUser(ctx context.Context, uid pgtype.UUID) error {
	userID := db.UUIDString(uid)
	live, err := s.q.ListLiveNodes(ctx, uid)
	if err != nil {
		return err
	}
	byPath := make(map[string]db.Node, len(live))
	for _, n := range live {
		if n.DiskPath.Valid {
			byPath[n.DiskPath.String] = n
		}
	}

	// Paths of trashed nodes: their files remain on disk until GC, but rescan
	// must NOT re-import them as "new" (otherwise soft-delete is resurrected).
	tombs, err := s.q.ListTombstonedNodePaths(ctx, uid)
	if err != nil {
		return err
	}
	tomb := make(map[string]bool, len(tombs))
	for _, p := range tombs {
		if p.Valid {
			tomb[p.String] = true
		}
	}

	entries, partial, err := s.st.Walk(userID)
	if err != nil {
		return err
	}
	var errs []error
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		seen[e.Rel] = true
		if _, ok := byPath[e.Rel]; ok {
			continue
		}
		if tomb[e.Rel] {
			continue // file belongs to a trashed node — leave it alone
		}
		var parentUUID pgtype.UUID
		if parent := parentDir(e.Rel); parent != userID {
			p, ok := byPath[parent]
			if !ok {
				continue // parent not yet seen (will be picked up on the next pass)
			}
			parentUUID = p.ID
		}
		node, err := s.createDiscovered(ctx, uid, parentUUID, e)
		if err != nil {
			// One broken entry (unreadable file, name conflict) must not stop
			// the import of everything else.
			errs = append(errs, fmt.Errorf("%s: %w", e.Rel, err))
			continue
		}
		byPath[e.Rel] = node
	}

	// An incomplete walk is no proof of deletion: skip the missing-node sweep
	// so nodes under unreadable directories are not falsely soft-deleted.
	if partial {
		errs = append(errs, errors.New("walk incomplete (unreadable directories), missing-node sweep skipped"))
		return errors.Join(errs...)
	}

	for path, n := range byPath {
		if seen[path] {
			continue
		}
		if err := s.markMissing(ctx, uid, n); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

func (s *FileService) createDiscovered(ctx context.Context, uid, parentUUID pgtype.UUID, e DiskEntry) (db.Node, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Node{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	name := baseName(e.Rel)
	var node db.Node
	if e.IsDir {
		node, err = qtx.CreateNode(ctx, db.CreateNodeParams{
			UserID: uid, ParentID: parentUUID, Name: name, IsDir: true, DiskPath: text(e.Rel),
		})
		if err != nil {
			return db.Node{}, mapInsertErr(err)
		}
	} else {
		size, sha, herr := s.hashFile(e.Rel)
		if herr != nil {
			return db.Node{}, herr
		}
		node, err = qtx.CreateNode(ctx, db.CreateNodeParams{
			UserID: uid, ParentID: parentUUID, Name: name, IsDir: false,
			Size: int8val(size), ContentHash: text(sha), DiskPath: text(e.Rel), Mime: text(detectMime(name)),
		})
		if err != nil {
			return db.Node{}, mapInsertErr(err)
		}
		// No snapshot: a freshly discovered file is version 1, and version 1 is the file.
	}
	if err := recordChange(ctx, qtx, uid, node.ID, "create", node.Version); err != nil {
		return db.Node{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Node{}, err
	}
	return node, nil
}

func (s *FileService) markMissing(ctx context.Context, uid pgtype.UUID, n db.Node) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	if err := qtx.SoftDeleteNode(ctx, n.ID); err != nil {
		return err
	}
	ver, err := qtx.BumpNodeVersion(ctx, n.ID)
	if err != nil {
		return err
	}
	if err := recordChange(ctx, qtx, uid, n.ID, "delete", ver); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *FileService) hashFile(rel string) (int64, string, error) {
	f, err := s.st.Open(rel)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func versionDir(userID, nodeID string) string {
	return ".versions/" + userID + "/" + nodeID
}

func baseName(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

func text(s string) pgtype.Text   { return pgtype.Text{String: s, Valid: true} }
func int8val(n int64) pgtype.Int8 { return pgtype.Int8{Int64: n, Valid: true} }

// tsval turns an optional client timestamp into a nullable column value. The zero time
// means "not supplied", and the query COALESCEs NULL to now().
func tsval(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// tmpName returns a unique relative path for staging an upload (outside the tree mirror).
func tmpName() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return ".tmp/" + hex.EncodeToString(b)
}

// conflictName builds the name for a conflict copy: "name (conflict, device, date).ext".
func conflictName(name, device string, ts time.Time) string {
	if device == "" {
		device = "device"
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	return base + " (conflict, " + device + ", " + ts.Format("2006-01-02 15-04-05") + ")" + ext
}

func parentDir(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return rel
}

func detectMime(name string) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// Trash returns the top-level deleted nodes for the current user (for the trash view).
func (s *FileService) Trash(ctx context.Context, userID string) ([]db.Node, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, ErrNotFound
	}
	return s.q.ListTrashNodes(ctx, uid)
}

// Purge permanently removes a node from the trash: subtree files and version snapshots
// from disk, subtree nodes rows (change_log/file_versions cascade). Owner only.
func (s *FileService) Purge(ctx context.Context, userID, nodeID string) error {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return ErrNotFound
	}
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return ErrNotFound
	}
	node, err := s.q.GetTrashedNodeForUser(ctx, db.GetTrashedNodeForUserParams{ID: nid, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	owner := node.UserID
	prefix := node.DiskPath.String

	subtree, err := s.q.ListTrashedSubtree(ctx, db.ListTrashedSubtreeParams{UserID: owner, Prefix: prefix})
	if err != nil {
		return err
	}
	// remove version snapshots for every trashed node from disk
	for _, r := range subtree {
		_ = s.st.Remove(versionDir(db.UUIDString(owner), db.UUIDString(r.ID)))
	}
	// remove files from disk: if a LIVE node exists at this path (the name was reused
	// after deletion) the physical content belongs to the live node — do not touch the disk.
	// Otherwise it is safe to remove the entire subtree.
	if _, lerr := s.q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: owner, Path: prefix}); errors.Is(lerr, pgx.ErrNoRows) {
		_ = s.st.Remove(prefix)
	} else if lerr != nil {
		return lerr
	}
	// else: path collision with a live node — leave disk untouched
	return s.q.HardDeleteSubtree(ctx, db.HardDeleteSubtreeParams{UserID: owner, Prefix: prefix})
}

// PurgeAll permanently empties the user's trash (irreversible).
func (s *FileService) PurgeAll(ctx context.Context, userID string) error {
	tops, err := s.Trash(ctx, userID)
	if err != nil {
		return err
	}
	for _, n := range tops {
		if err := s.Purge(ctx, userID, db.UUIDString(n.ID)); err != nil {
			return err
		}
	}
	return nil
}

// Undelete restores a node and its subtree from the trash. If the parent is also deleted
// → restore to root; if the name is taken by a live node → append " (restored)". Owner only.
func (s *FileService) Undelete(ctx context.Context, userID, nodeID string) (db.Node, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Node{}, ErrNotFound
	}
	nid, err := db.ParseUUID(nodeID)
	if err != nil {
		return db.Node{}, ErrNotFound
	}
	node, err := s.q.GetTrashedNodeForUser(ctx, db.GetTrashedNodeForUserParams{ID: nid, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Node{}, ErrNotFound
	}
	if err != nil {
		return db.Node{}, err
	}
	owner := node.UserID
	oldRel := node.DiskPath.String

	// target parent: the original if it is alive; otherwise root (prefix = user UUID).
	parentPrefix := userID
	parentID := node.ParentID
	toRoot := false
	if node.ParentID.Valid {
		p, perr := s.q.GetNode(ctx, node.ParentID)
		switch {
		case errors.Is(perr, pgx.ErrNoRows):
			toRoot, parentID = true, pgtype.UUID{}
		case perr != nil:
			return db.Node{}, perr
		default:
			parentPrefix = p.DiskPath.String
		}
	}

	// name collision with a live node in the target folder → add suffix.
	name := node.Name
	newRel := parentPrefix + "/" + name
	if _, gerr := s.q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: owner, Path: newRel}); gerr == nil {
		name = node.Name + " (restored)"
		newRel = parentPrefix + "/" + name
	} else if !errors.Is(gerr, pgx.ErrNoRows) {
		return db.Node{}, gerr
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return db.Node{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	// While the subtree is "deleted", the partial unique index is inactive — update path/parent/name in DB.
	// IMPORTANT: rewrite only tombstoned nodes; otherwise we'd clobber a LIVE node
	// sharing the same disk_path (name reused after deletion).
	if newRel != oldRel {
		if err := qtx.RewriteTombstonedSubtreePaths(ctx, db.RewriteTombstonedSubtreePathsParams{UserID: owner, OldPrefix: oldRel, NewPrefix: newRel}); err != nil {
			return db.Node{}, err
		}
	}
	if toRoot {
		if _, err := qtx.UpdateNodeParent(ctx, db.UpdateNodeParentParams{ID: node.ID, ParentID: parentID}); err != nil {
			return db.Node{}, err
		}
	}
	if name != node.Name {
		if _, err := qtx.UpdateNodeName(ctx, db.UpdateNodeNameParams{ID: node.ID, Name: name}); err != nil {
			return db.Node{}, err
		}
	}
	// Clear deleted_at — this is where the unique index fires on a name collision.
	if err := qtx.UndeleteSubtree(ctx, db.UndeleteSubtreeParams{UserID: owner, Prefix: newRel}); err != nil {
		return db.Node{}, mapInsertErr(err)
	}
	ver, err := qtx.BumpNodeVersion(ctx, node.ID)
	if err != nil {
		return db.Node{}, err
	}
	if err := recordChange(ctx, qtx, owner, node.ID, "create", ver); err != nil {
		return db.Node{}, err
	}
	// Move the disk AFTER all DB checks (last fallible step before commit).
	// If the original physical path is shared by a LIVE node (name was reused) —
	// copy (don't steal its file); otherwise move.
	if newRel != oldRel {
		_, lerr := s.q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: owner, Path: oldRel})
		switch {
		case errors.Is(lerr, pgx.ErrNoRows):
			if err := s.st.Move(oldRel, newRel); err != nil {
				return db.Node{}, err
			}
		case lerr != nil:
			return db.Node{}, lerr
		default:
			if err := s.st.Copy(oldRel, newRel); err != nil {
				return db.Node{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Node{}, err
	}
	return s.q.GetNode(ctx, node.ID)
}

// mapInsertErr converts a unique index violation on the name into ErrNameTaken.
func mapInsertErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameTaken
	}
	return err
}

// --- Sync-push helpers (stage 3.2b) ---

// userRelToDisk reconstructs the full disk_path from a user-relative path.
func userRelToDisk(userID, rel string) string { return userID + "/" + rel }

// PushByPath performs a sync-push by user-relative path: resolves/creates the directory
// chain and uploads content via Push (conflict-aware by baseVersion).
func (s *FileService) PushByPath(ctx context.Context, userID, relPath string, baseVersion *int64, r io.Reader) (PushResult, error) {
	return s.PushByPathWithMeta(ctx, userID, relPath, baseVersion, r, PushMeta{})
}

// PushByPathWithMeta is PushByPath plus client-supplied metadata.
func (s *FileService) PushByPathWithMeta(ctx context.Context, userID, relPath string, baseVersion *int64, r io.Reader, meta PushMeta) (PushResult, error) {
	rel := strings.Trim(filepath.ToSlash(relPath), "/")
	if rel == "" {
		return PushResult{}, ErrNotFound
	}
	dir, name := path.Split(rel)
	parentID, err := s.ensureDirChain(ctx, userID, strings.Trim(dir, "/"))
	if err != nil {
		return PushResult{}, err
	}
	return s.PushWithMeta(ctx, userID, parentID, name, baseVersion, "desktop", r, meta)
}

// ensureDirChain idempotently creates the directory chain for dir (user-relative) and
// returns the node ID of the leaf directory (nil = root).
func (s *FileService) ensureDirChain(ctx context.Context, userID, dir string) (*string, error) {
	if dir == "" {
		return nil, nil
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, ErrNotFound
	}
	var parentID *string
	prefix := ""
	for _, seg := range strings.Split(dir, "/") {
		if seg == "" {
			continue
		}
		if prefix == "" {
			prefix = seg
		} else {
			prefix = prefix + "/" + seg
		}
		node, err := s.q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: uid, Path: userRelToDisk(userID, prefix)})
		if errors.Is(err, pgx.ErrNoRows) {
			created, cerr := s.CreateFolder(ctx, userID, parentID, seg)
			if cerr != nil {
				return nil, cerr
			}
			id := db.UUIDString(created.ID)
			parentID = &id
			continue
		}
		if err != nil {
			return nil, err
		}
		id := db.UUIDString(node.ID)
		parentID = &id
	}
	return parentID, nil
}

// EnsureDirByPath idempotently creates a directory at the given user-relative path.
func (s *FileService) EnsureDirByPath(ctx context.Context, userID, relPath string) (db.Node, error) {
	rel := strings.Trim(filepath.ToSlash(relPath), "/")
	if rel == "" {
		return db.Node{}, ErrNotFound
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return db.Node{}, ErrNotFound
	}
	if existing, gerr := s.q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: uid, Path: userRelToDisk(userID, rel)}); gerr == nil {
		return existing, nil
	}
	dir, name := path.Split(rel)
	parentID, err := s.ensureDirChain(ctx, userID, strings.Trim(dir, "/"))
	if err != nil {
		return db.Node{}, err
	}
	return s.CreateFolder(ctx, userID, parentID, name)
}

// DeleteByPath soft-deletes a node by user-relative path (idempotent).
func (s *FileService) DeleteByPath(ctx context.Context, userID, relPath string) error {
	rel := strings.Trim(filepath.ToSlash(relPath), "/")
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return ErrNotFound
	}
	node, err := s.q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: uid, Path: userRelToDisk(userID, rel)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.Delete(ctx, userID, db.UUIDString(node.ID))
}
