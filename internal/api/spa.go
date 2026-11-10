package api

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves the embedded UI static assets. Mounted under /app/ (see router.go),
// so r.URL.Path arrives here without the prefix. Directory routes (files, admin/users)
// are served their own index.html directly without a 301; unknown paths (client-side
// history-mode routes) fall back to the root index.html.
func spaHandler(ui fs.FS) http.Handler {
	fileSrv := http.FileServer(http.FS(ui))
	// serveHTML serves an HTML file as raw bytes: http.FileServer canonicalises
	// "*/index.html" to "./" with a 301, which breaks the path under the /app prefix.
	serveHTML := func(w http.ResponseWriter, name string) {
		f, err := ui.Open(name)
		if err != nil {
			http.Error(w, "UI not built", http.StatusNotFound)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache") // names the hashed bundles: always fresh
		_, _ = io.Copy(w, f)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			serveHTML(w, "index.html")
			return
		}
		f, err := ui.Open(name)
		if err != nil {
			serveHTML(w, "index.html") // SPA-fallback
			return
		}
		info, statErr := f.Stat()
		_ = f.Close()
		if statErr == nil && info.IsDir() {
			if idx := path.Join(name, "index.html"); fileExists(ui, idx) {
				serveHTML(w, idx) // directory route (files, admin/users)
			} else {
				serveHTML(w, "index.html")
			}
			return
		}
		// Embedded files have no modification time, so there is nothing to revalidate
		// by: without a header the browser fetched every bundle again on each load.
		// Names under _nuxt/ carry a content hash and never change their content.
		if strings.HasPrefix(name, "_nuxt/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileSrv.ServeHTTP(w, r) // regular static file (_nuxt/*, *.html)
	})
}

func fileExists(ui fs.FS, name string) bool {
	f, err := ui.Open(name)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
