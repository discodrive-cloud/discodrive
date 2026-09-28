package main

import (
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"
)

// minCompressSize: responses that declare a smaller Content-Length are sent as is —
// gzip's own framing would eat most of the saving.
const minCompressSize = 1024

// compressibleTypes are the text responses worth compressing: API JSON, the web UI, and
// the XML of WebDAV and Subsonic. Files are left alone — media and archives are already
// compressed, and they must keep Range and the zero-copy (sendfile) path.
var compressibleTypes = map[string]bool{
	"application/json":       true,
	"text/html":              true,
	"text/css":               true,
	"text/javascript":        true,
	"application/javascript": true,
	"application/xml":        true,
	"text/xml":               true,
}

var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
	return w
}}

// compressResponses gzips GET and PROPFIND responses of compressible types for clients
// that accept it. Other methods are never compressed: tokens, app passwords and
// one-time codes are only ever returned to POSTs, which keeps them out of reach of
// compression side channels (BREACH).
func compressResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != "PROPFIND") || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip (not "gzip;q=0").
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}

// gzipWriter decides at WriteHeader — when the status, type and length are known —
// whether to compress. When it does not, writes and ReadFrom go straight to the
// underlying writer, so file downloads keep sendfile.
type gzipWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	decided  bool
	compress bool
}

func (w *gzipWriter) decide(code int) {
	if w.decided {
		return
	}
	w.decided = true
	h := w.Header()
	h.Add("Vary", "Accept-Encoding")
	if (code != http.StatusOK && code != http.StatusMultiStatus) || h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return
	}
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || !compressibleTypes[mt] {
		return
	}
	if cl := h.Get("Content-Length"); cl != "" {
		if n, err := strconv.Atoi(cl); err == nil && n < minCompressSize {
			return
		}
	}
	w.compress = true
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	w.gz = gzipPool.Get().(*gzip.Writer)
	w.gz.Reset(w.ResponseWriter)
}

func (w *gzipWriter) WriteHeader(code int) {
	w.decide(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipWriter) Write(p []byte) (int, error) {
	if !w.decided {
		if w.Header().Get("Content-Type") == "" {
			// net/http would sniff the type on this first write; do it here so the
			// decision sees it.
			w.Header().Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.compress {
		return w.gz.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

// ReadFrom keeps io.Copy (and so http.ServeContent) on the underlying writer's
// ReadFrom — sendfile for files — whenever the response is not compressed.
func (w *gzipWriter) ReadFrom(src io.Reader) (int64, error) {
	if !w.decided {
		if w.Header().Get("Content-Type") == "" {
			return io.Copy(writerOnly{w}, src) // let Write sniff the type
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.compress {
		return io.Copy(w.gz, src)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(w.ResponseWriter, src)
}

func (w *gzipWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipWriter) close() {
	if w.gz == nil {
		return
	}
	_ = w.gz.Close()
	w.gz.Reset(io.Discard)
	gzipPool.Put(w.gz)
	w.gz = nil
}

// writerOnly hides ReadFrom so io.Copy goes through Write.
type writerOnly struct{ io.Writer }
