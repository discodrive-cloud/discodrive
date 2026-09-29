package webdav

import (
	"context"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync/atomic"

	"golang.org/x/net/webdav"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// ctxKey holds context keys populated by the Auth middleware (see auth.go, Task 7).
type ctxKey int

const (
	ctxUserKey ctxKey = iota
	ctxDeviceKey
	ctxPutLenKey
	ctxMoveKey
)

// moveReplace carries a MOVE's "Overwrite: T" from RemoveAll to Rename. x/net/webdav
// performs it as two calls — RemoveAll(destination), then Rename(source, destination) —
// so a rename that failed used to leave the destination in the trash and the source
// where it was. Within a MOVE, RemoveAll only notes the node at the destination, and
// Rename trashes it in the same transaction that moves the source into its place.
type moveReplace struct {
	name string // cleaned DAV path of the destination
	id   string // node id found there
}

// withMove marks ctx as serving a MOVE request (see moveReplace).
func withMove(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxMoveKey, &moveReplace{})
}

func moveOf(ctx context.Context) *moveReplace {
	mv, _ := ctx.Value(ctxMoveKey).(*moveReplace)
	return mv
}

// WithDeclaredLength returns a context carrying the declared body length of an upload
// (the request's Content-Length, or -1 when the client did not state one). Handler
// installs it for every PUT; writeFile.Close uses it to refuse a body that did not
// arrive in full instead of publishing a truncated file.
func WithDeclaredLength(ctx context.Context, n int64) context.Context {
	return context.WithValue(ctx, ctxPutLenKey, n)
}

// declaredLength reads the value set by WithDeclaredLength; -1 means "unknown".
func declaredLength(ctx context.Context) int64 {
	if n, ok := ctx.Value(ctxPutLenKey).(int64); ok {
		return n
	}
	return -1
}

// FileSystem is a webdav.FileSystem backed by the sync core, scoped to a single user.
type FileSystem = webdav.FileSystem

type fsImpl struct {
	svc    *storage.FileService
	userID string
	// dbCalls counts node lookups and folder listings, so tests can tell how a request
	// scales with folder size.
	dbCalls atomic.Int64
}

// NewFileSystem constructs a webdav.FileSystem for a specific user's file tree.
func NewFileSystem(svc *storage.FileService, userID string) webdav.FileSystem {
	return &fsImpl{svc: svc, userID: userID}
}

func clean(name string) string { return path.Clean("/" + strings.TrimPrefix(name, "/")) }

// isMacJunk detects macOS Finder housekeeping files created during WebDAV copies:
// AppleDouble sidecar files (._name, resource fork/xattrs) and .DS_Store (folder view metadata).
// We do not materialize them — writes are accepted and discarded to keep
// the file tree and web UI clean. The actual file data is stored as normal.
func isMacJunk(name string) bool {
	return name == ".DS_Store" || strings.HasPrefix(name, "._")
}

// deviceOf reads the deviceID from ctx (set by the middleware); falls back to "webdav" in tests.
func deviceOf(ctx context.Context) string {
	if d, ok := ctx.Value(ctxDeviceKey).(string); ok && d != "" {
		return d
	}
	return "webdav"
}

// lookup resolves a cleaned DAV path to its node, answering from the request's memo when
// a folder listing earlier in the same request already returned it.
func (f *fsImpl) lookup(ctx context.Context, name string) (db.Node, error) {
	memo := memoFrom(ctx)
	if n, ok := memo.get(name); ok {
		return n, nil
	}
	f.dbCalls.Add(1)
	n, err := f.svc.NodeByPath(ctx, f.userID, name)
	if err == nil {
		memo.put(name, n)
	}
	return n, err
}

func (f *fsImpl) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	name = clean(name)
	if name == "/" {
		return nodeInfo{name: "/", dir: true}, nil
	}
	if isMacJunk(path.Base(name)) {
		return nil, os.ErrNotExist // macOS junk files are not exposed
	}
	n, err := f.lookup(ctx, name)
	if err != nil {
		return nil, mapErr(err)
	}
	return infoFromNode(n), nil
}

func (f *fsImpl) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	name = clean(name)
	parentID, leaf, err := f.parentOf(ctx, name)
	if err != nil {
		return err
	}
	if isMacJunk(leaf) {
		return nil // pretend it was created — nothing is materialized
	}
	_, err = f.svc.CreateFolder(ctx, f.userID, parentID, leaf)
	memoFrom(ctx).clear()
	return mapErr(err)
}

func (f *fsImpl) RemoveAll(ctx context.Context, name string) error {
	name = clean(name)
	if isMacJunk(path.Base(name)) {
		return nil // macOS junk files do not exist — nothing to delete
	}
	n, err := f.lookup(ctx, name)
	if err != nil {
		return mapErr(err)
	}
	if mv := moveOf(ctx); mv != nil {
		// The destination of a MOVE: Rename replaces it atomically (see moveReplace).
		mv.name, mv.id = name, db.UUIDString(n.ID)
		return nil
	}
	defer memoFrom(ctx).clear()
	return mapErr(f.svc.Delete(ctx, f.userID, db.UUIDString(n.ID)))
}

// Rename implements WebDAV MOVE: the move, the rename and (with "Overwrite: T") the
// replacement of the destination are one core operation, so a failure leaves both
// source and destination as they were.
func (f *fsImpl) Rename(ctx context.Context, oldName, newName string) error {
	oldName, newName = clean(oldName), clean(newName)
	if isMacJunk(path.Base(oldName)) || isMacJunk(path.Base(newName)) {
		return nil // macOS junk files are not materialized — nothing to move
	}
	n, err := f.lookup(ctx, oldName)
	if err != nil {
		return mapErr(err)
	}
	defer memoFrom(ctx).clear()
	parentID, leaf, err := f.parentOf(ctx, newName)
	if err != nil {
		return err
	}
	replace := ""
	if mv := moveOf(ctx); mv != nil && mv.name == newName {
		replace = mv.id
	}
	_, err = f.svc.Relocate(ctx, f.userID, db.UUIDString(n.ID), parentID, leaf, replace)
	return mapErr(err)
}

func (f *fsImpl) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	name = clean(name)
	leaf := path.Base(name)
	if flag&os.O_WRONLY != 0 || flag&os.O_RDWR != 0 {
		if isMacJunk(leaf) {
			return &discardFile{name: leaf}, nil // accept and discard
		}
		memoFrom(ctx).clear() // the path is about to change (writeFile clears again on Close)
		parentID, leaf, err := f.parentOf(ctx, name)
		if err != nil {
			return nil, err
		}
		tmp, err := os.CreateTemp("", "kfdav-*")
		if err != nil {
			return nil, err
		}
		return &writeFile{ctx: ctx, svc: f.svc, userID: f.userID, deviceID: deviceOf(ctx),
			parentID: parentID, name: leaf, tmp: tmp, expect: declaredLength(ctx)}, nil
	}
	if name == "/" {
		return f.dir(ctx, "/", nodeInfo{name: "/", dir: true}, ""), nil
	}
	if isMacJunk(leaf) {
		return nil, os.ErrNotExist // macOS junk files do not exist for reading
	}
	n, err := f.lookup(ctx, name)
	if err != nil {
		return nil, mapErr(err)
	}
	if n.IsDir {
		return f.dir(ctx, name, infoFromNode(n), db.UUIDString(n.ID)), nil
	}
	// Lazy: defer opening the backing content until the first Read/Seek so a metadata-only
	// PROPFIND never touches file bytes (see readFile).
	id := db.UUIDString(n.ID)
	return &readFile{
		info: infoFromNode(n),
		open: func() (*os.File, error) {
			_, file, err := f.svc.Open(ctx, f.userID, id)
			if err != nil {
				return nil, mapErr(err)
			}
			return file, nil
		},
	}, nil
}

// dir builds a directory webdav.File for the folder at name. Its children are listed
// only when Readdir asks for them: PROPFIND opens every child folder to read its
// properties, and listing each of those was wasted work. The listing also fills the
// request's memo, so the Stat/OpenFile that follow for each child need no query.
func (f *fsImpl) dir(ctx context.Context, name string, info nodeInfo, nodeID string) webdav.File {
	return &dirFile{info: info, load: func() ([]fs.FileInfo, error) {
		f.dbCalls.Add(1)
		var (
			kids []db.Node
			err  error
		)
		if nodeID == "" {
			kids, err = f.svc.RootChildren(ctx, f.userID)
		} else {
			kids, err = f.svc.ListChildren(ctx, f.userID, nodeID)
		}
		if err != nil {
			return nil, mapErr(err)
		}
		memo := memoFrom(ctx)
		children := make([]fs.FileInfo, 0, len(kids))
		for _, k := range kids {
			// Don't expose macOS junk nodes (.DS_Store, ._*) over WebDAV. Besides being noise,
			// Stat()/OpenFile() report them as non-existent, which would abort the whole PROPFIND
			// walk if they slipped into a listing.
			if isMacJunk(k.Name) {
				continue
			}
			memo.put(path.Join(name, k.Name), k)
			children = append(children, infoFromNode(k))
		}
		return children, nil
	}}
}

// parentOf resolves the parent directory of a path → its nodeID (nil = root) and the leaf name.
func (f *fsImpl) parentOf(ctx context.Context, name string) (*string, string, error) {
	dir, leaf := path.Dir(name), path.Base(name)
	if dir == "/" || dir == "." {
		return nil, leaf, nil
	}
	pn, err := f.lookup(ctx, dir)
	if err != nil {
		return nil, "", mapErr(err)
	}
	id := db.UUIDString(pn.ID)
	return &id, leaf, nil
}
