package webdav

import (
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"

	"golang.org/x/net/webdav"

	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

// Locks are scoped per user: memLS keys locks by path (after stripping the /dav prefix),
// so a shared LockSystem would collide on same-named files of different users
// (user A's LOCK → 423 for user B). One memLS per userID eliminates this.
var (
	lsMu     sync.Mutex
	lsByUser = map[string]webdav.LockSystem{}
)

func userLockSystem(userID string) webdav.LockSystem {
	lsMu.Lock()
	defer lsMu.Unlock()
	ls, ok := lsByUser[userID]
	if !ok {
		ls = webdav.NewMemLS()
		lsByUser[userID] = ls
	}
	return ls
}

// Handler assembles a WebDAV handler: per-request FileSystem and LockSystem
// are scoped to the userID from the context (set by Auth). LockSystem is in-memory
// (class 2 for Finder read-write), one instance per user.
func Handler(svc *storage.FileService, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := UserID(r.Context())
		normalizeDestinationHost(r)
		// Record what the client promised to send so the write path can refuse a PUT whose
		// body did not arrive in full rather than publishing the partial bytes (see
		// writeFile.Close). ContentLength is -1 for chunked bodies — then the length check
		// is skipped and only the context-cancellation guard applies.
		if r.Method == http.MethodPut {
			// Refuse a file that does not fit before reading the body: WebDAV clients
			// (Finder, rclone, the desktop sync) always declare a length, and 507 with
			// a reason is a far better answer than letting them push gigabytes only to
			// fail at commit time. A chunked PUT has no declared length: writeFile
			// stops buffering it at the allowance and the client gets 507 all the same.
			// macOS junk files (._*, .DS_Store) are accepted and thrown away, so they
			// cost nothing and must not be refused when the quota is full.
			if r.ContentLength > 0 && !isMacJunk(path.Base(r.URL.Path)) {
				if err := svc.CheckQuota(r.Context(), uid, r.ContentLength); errors.Is(err, quota.ErrExceeded) {
					http.Error(w, err.Error(), http.StatusInsufficientStorage)
					return
				} else if err != nil {
					log.Printf("webdav: quota check %s: %v", r.URL.Path, err)
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
			}
			ctx, pq := withPutQuota(WithDeclaredLength(r.Context(), r.ContentLength))
			r = r.WithContext(ctx)
			w = &quotaStatusWriter{ResponseWriter: w, pq: pq}
		}
		if r.Method == "MOVE" {
			r = r.WithContext(withMove(r.Context()))
		}
		h := &webdav.Handler{
			Prefix:     prefix,
			FileSystem: NewFileSystem(svc, uid),
			LockSystem: userLockSystem(uid),
			// Surface unexpected handler errors. Skip os.IsNotExist: macOS Finder probes a
			// flurry of non-existent paths (.Spotlight-V100, eTokenVirtual, …) whose 404s
			// are normal and would otherwise spam the log.
			Logger: func(req *http.Request, err error) {
				if err != nil && !os.IsNotExist(err) {
					log.Printf("webdav: %s %s: %v", req.Method, req.URL.Path, err)
				}
			},
		}
		h.ServeHTTP(w, r.WithContext(withNodeMemo(r.Context())))
	})
}

// normalizeDestinationHost rewrites the MOVE/COPY Destination header to a host-relative
// path when it targets the same host as the request. golang.org/x/net/webdav rejects a
// Destination whose host differs from r.Host with 502 Bad Gateway. Behind a reverse proxy
// that drops the port (nginx "proxy_set_header Host $host" yields "host", while the client
// sends "host:port" in Destination), this strict check fails and Finder reports error -43.
// Comparing host names without the port and stripping to the path sidesteps it.
func normalizeDestinationHost(r *http.Request) {
	d := r.Header.Get("Destination")
	if d == "" {
		return
	}
	u, err := url.Parse(d)
	if err != nil || u.Host == "" {
		return
	}
	if sameHostname(u.Host, r.Host) {
		r.Header.Set("Destination", u.EscapedPath())
	}
}

// sameHostname compares two host[:port] strings ignoring the port.
func sameHostname(a, b string) bool {
	if h, _, err := net.SplitHostPort(a); err == nil {
		a = h
	}
	if h, _, err := net.SplitHostPort(b); err == nil {
		b = h
	}
	return strings.EqualFold(a, b)
}

// quotaStatusWriter turns the 405 x/net/webdav sends for a failed body copy into 507 when
// the copy failed because the body ran past the user's allowance.
type quotaStatusWriter struct {
	http.ResponseWriter
	pq       *putQuota
	replaced bool
}

func (q *quotaStatusWriter) WriteHeader(code int) {
	if code == http.StatusMethodNotAllowed && q.pq.exceeded {
		q.replaced = true
		http.Error(q.ResponseWriter, quota.ErrExceeded.Error(), http.StatusInsufficientStorage)
		return
	}
	q.ResponseWriter.WriteHeader(code)
}

func (q *quotaStatusWriter) Write(p []byte) (int, error) {
	if q.replaced {
		return len(p), nil // the 405 status text; the 507 body is already written
	}
	return q.ResponseWriter.Write(p)
}
