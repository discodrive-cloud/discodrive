package webdav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"time"

	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

// nodeInfo implements os.FileInfo on top of a tree node.
type nodeInfo struct {
	name  string
	size  int64
	dir   bool
	mtime time.Time
	ctype string // stored MIME type, if known
}

func (i nodeInfo) Name() string { return i.name }
func (i nodeInfo) Size() int64  { return i.size }
func (i nodeInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i nodeInfo) ModTime() time.Time { return i.mtime }
func (i nodeInfo) IsDir() bool        { return i.dir }
func (i nodeInfo) Sys() any           { return nil }

// ContentType implements webdav.ContentTyper so PROPFIND uses the node's stored MIME (or a
// type derived from the extension) instead of OPENING the file to sniff its content. Without
// this, x/net/webdav opens every regular file during a directory PROPFIND; any file whose
// content can't be read then aborts the whole listing (manifesting as a "superfluous
// WriteHeader" and a directory Finder cannot enumerate).
func (i nodeInfo) ContentType(context.Context) (string, error) {
	if i.ctype != "" {
		return i.ctype, nil
	}
	if ct := mime.TypeByExtension(path.Ext(i.name)); ct != "" {
		return ct, nil
	}
	return "application/octet-stream", nil
}

func infoFromNode(n db.Node) nodeInfo {
	var sz int64
	if n.Size.Valid {
		sz = n.Size.Int64
	}
	mt := time.Now()
	if n.ModifiedAt.Valid {
		mt = n.ModifiedAt.Time
	}
	return nodeInfo{name: n.Name, size: sz, dir: n.IsDir, mtime: mt, ctype: n.Mime.String}
}

// readFile is a webdav.File for reads (GET). The backing content file is opened LAZILY on
// the first Read/Seek: a PROPFIND only needs Stat() metadata, and x/net/webdav opens every
// listed entry (webdav/prop.go props()) just to read its properties. Opening content eagerly
// there meant a single unreadable file aborted the whole directory listing (superfluous
// WriteHeader; Finder could not enumerate). Metadata comes from the index, not the bytes.
type readFile struct {
	info nodeInfo
	open func() (*os.File, error) // opens the backing content on demand
	f    *os.File                 // nil until first Read/Seek
}

func (f *readFile) ensure() error {
	if f.f == nil {
		opened, err := f.open()
		if err != nil {
			return err
		}
		f.f = opened
	}
	return nil
}

func (f *readFile) Read(p []byte) (int, error) {
	if err := f.ensure(); err != nil {
		return 0, err
	}
	return f.f.Read(p)
}

func (f *readFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.ensure(); err != nil {
		return 0, err
	}
	return f.f.Seek(offset, whence)
}

func (f *readFile) Close() error {
	if f.f != nil {
		return f.f.Close()
	}
	return nil
}

func (f *readFile) Write([]byte) (int, error)          { return 0, errReadOnly }
func (f *readFile) Readdir(int) ([]fs.FileInfo, error) { return nil, errNotDir }
func (f *readFile) Stat() (fs.FileInfo, error)         { return f.info, nil }

// dirFile is a webdav.File for directories (PROPFIND): serves children via Readdir.
type dirFile struct {
	info     nodeInfo
	load     func() ([]fs.FileInfo, error) // lists the children on first Readdir
	loaded   bool
	children []fs.FileInfo
}

func (d *dirFile) Close() error                   { return nil }
func (d *dirFile) Read([]byte) (int, error)       { return 0, errIsDir }
func (d *dirFile) Seek(int64, int) (int64, error) { return 0, errIsDir }
func (d *dirFile) Write([]byte) (int, error)      { return 0, errIsDir }
func (d *dirFile) Stat() (fs.FileInfo, error)     { return d.info, nil }
func (d *dirFile) Readdir(count int) ([]fs.FileInfo, error) {
	if !d.loaded {
		children, err := d.load()
		if err != nil {
			return nil, err
		}
		d.children, d.loaded = children, true
	}
	if count <= 0 {
		return d.children, nil
	}
	if len(d.children) == 0 {
		return nil, io.EOF
	}
	n := count
	if n > len(d.children) {
		n = len(d.children)
	}
	out := d.children[:n]
	d.children = d.children[n:]
	return out, nil
}

// writeFile is a webdav.File for PUT: buffers into a temp file, then Push on Close.
type writeFile struct {
	ctx      context.Context
	svc      *storage.FileService
	userID   string
	deviceID string
	parentID *string
	name     string
	tmp      *os.File
	expect   int64 // declared Content-Length; -1 when the client did not state one
	written  int64 // bytes actually buffered so far
	limit    int64 // the user's allowance when the PUT began; quota.Unlimited if none
	failed   error // the first Write error: the body is not whole, Close must not publish it
}

func (w *writeFile) Write(p []byte) (int, error) {
	// The user's other PUTs in flight share the limit (see spooled).
	if !spoolTake(w.userID, int64(len(p)), w.limit) {
		if pq := putQuotaOf(w.ctx); pq != nil {
			pq.exceeded = true
		}
		w.failed = fmt.Errorf("upload of %q: %w", w.name, quota.ErrExceeded)
		return 0, w.failed
	}
	n, err := w.tmp.Write(p)
	spoolGive(w.userID, int64(len(p)-n))
	w.written += int64(n)
	if err != nil && w.failed == nil {
		w.failed = fmt.Errorf("upload of %q stopped after %d bytes: %w", w.name, w.written, err)
	}
	return n, err
}
func (w *writeFile) Read([]byte) (int, error)            { return 0, os.ErrInvalid }
func (w *writeFile) Seek(o int64, wh int) (int64, error) { return w.tmp.Seek(o, wh) }
func (w *writeFile) Readdir(int) ([]fs.FileInfo, error)  { return nil, errNotDir }
func (w *writeFile) Stat() (fs.FileInfo, error) {
	return nodeInfo{name: w.name, dir: false, mtime: time.Now()}, nil
}
func (w *writeFile) Close() error {
	defer func() { _ = w.tmp.Close(); _ = os.Remove(w.tmp.Name()); spoolGive(w.userID, w.written); w.written = 0 }()
	// x/net/webdav's handlePut calls Close() even when copying the request body FAILED
	// (webdav.go: closeErr is taken before copyErr is inspected) — and this Close is what
	// publishes the file. Committing unconditionally turns any mid-transfer failure into a
	// silently truncated file that looks complete: it appears in listings at its short
	// length, and content_hash is computed over the bytes that did arrive, so no later
	// integrity check can tell it apart from a genuine upload. Verify before publishing.
	if err := w.ctx.Err(); err != nil {
		return fmt.Errorf("upload of %q aborted after %d bytes: %w", w.name, w.written, err)
	}
	// A chunked body declares no length, so the check below cannot catch it: a body cut
	// off at the allowance or by a full disk would be published as it stood (down to an
	// empty file over a good one).
	if w.failed != nil {
		return w.failed
	}
	if w.expect >= 0 && w.written != w.expect {
		return fmt.Errorf("upload of %q incomplete: received %d of %d declared bytes",
			w.name, w.written, w.expect)
	}
	if _, err := w.tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := w.svc.Push(w.ctx, w.userID, w.parentID, w.name, nil, w.deviceID, w.tmp)
	memoFrom(w.ctx).clear() // the handler Stats the file right after, for its ETag
	return mapErr(err)
}

// discardFile is a webdav.File for macOS junk files (._*, .DS_Store):
// writes are accepted and silently dropped; no node is created.
type discardFile struct{ name string }

func (discardFile) Close() error                       { return nil }
func (discardFile) Read([]byte) (int, error)           { return 0, io.EOF }
func (discardFile) Seek(int64, int) (int64, error)     { return 0, nil }
func (d discardFile) Write(p []byte) (int, error)      { return len(p), nil }
func (discardFile) Readdir(int) ([]fs.FileInfo, error) { return nil, errNotDir }
func (d discardFile) Stat() (fs.FileInfo, error) {
	return nodeInfo{name: d.name, mtime: time.Now()}, nil
}

var (
	errIsDir    = errors.New("is a directory")
	errNotDir   = errors.New("not a directory")
	errReadOnly = errors.New("file is read-only")
)

// mapErr translates core errors to os-level errors that webdav.Handler understands.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrNotFound):
		// Wrap in *os.PathError on purpose: during a PROPFIND walk x/net/webdav SKIPS a
		// child that returns a *os.PathError (webdav.go handlePropfindError) but ABORTS the
		// whole listing for a bare error. So one unresolvable child (listed by RootChildren
		// but not findable by NodeByPath) must not nuke the directory. os.IsNotExist still
		// reports true through the wrapper, so a direct request to a missing path is 404.
		return &os.PathError{Op: "stat", Path: "", Err: os.ErrNotExist}
	case errors.Is(err, storage.ErrNotOwner):
		return os.ErrPermission
	case errors.Is(err, storage.ErrNameTaken):
		return os.ErrExist
	case errors.Is(err, storage.ErrNotDir),
		errors.Is(err, storage.ErrInvalidName),
		errors.Is(err, storage.ErrCycle):
		return os.ErrInvalid
	default:
		return err
	}
}
