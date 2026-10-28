package api

import (
	"net/http"
	"strings"
)

// Sections are the optional sections of the service an operator may switch off for
// everyone (MUSIC_ENABLED, BOOKS_ENABLED, PHOTOS_ENABLED). true = available.
type Sections struct {
	Music  bool `json:"music"`
	Books  bool `json:"books"`
	Photos bool `json:"photos"`
}

// AllSections is the default: nothing switched off.
var AllSections = Sections{Music: true, Books: true, Photos: true}

// Option tunes the router beyond NewRouter's arguments.
type Option func(*Server)

// WithSections switches sections off server-wide.
func WithSections(sec Sections) Option {
	return func(s *Server) { s.sections = sec }
}

// GET /me/features — which optional sections this server offers at all. A section that
// is off is not shown and cannot be turned on.
func (s *Server) handleGetFeatures(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sections)
}

// sectionOf names the optional section a request path belongs to ("" for none).
func sectionOf(p string) string {
	under := func(root string) bool { return p == root || strings.HasPrefix(p, root+"/") }
	switch {
	case under("/me/music"), under("/rest"):
		return "music"
	case under("/me/ebooks"), under("/opds"), under("/users"), under("/syncs"):
		return "books"
	case under("/me/photos"), under("/admin/photos"):
		return "photos"
	}
	return ""
}

// sectionGate closes the routes of switched-off sections: everything answers 404,
// except the settings reads, which say the section is off (older clients and the
// mobile apps read them to decide what to show).
func (s *Server) sectionGate(next http.Handler) http.Handler {
	if s.sections == AllSections {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sec := sectionOf(r.URL.Path)
		off := (sec == "music" && !s.sections.Music) || (sec == "books" && !s.sections.Books) ||
			(sec == "photos" && !s.sections.Photos)
		if !off {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet {
			switch r.URL.Path {
			case "/me/music", "/me/ebooks":
				writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "disabled": true})
				return
			case "/me/photos/v1/settings":
				writeJSON(w, http.StatusOK, map[string]any{"available": false, "enabled": false, "disabled": true,
					"sources": []any{}, "indexing": map[string]any{}})
				return
			}
		}
		writeError(w, http.StatusNotFound, "this section is turned off on this server")
	})
}
