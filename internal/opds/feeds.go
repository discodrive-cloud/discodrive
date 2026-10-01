package opds

import (
	"net/http"
	"time"

	"discodrive/internal/db"
)

// now returns the current time formatted as RFC3339 (used for feed updated fields).
func now() string { return time.Now().UTC().Format(time.RFC3339) }

// selfStartLinks returns the standard self + start links for navigation feeds.
func selfStartLinks(self string) []atomLink {
	return []atomLink{
		{Rel: "self", Href: self, Type: mediaTypeNavigation},
		{Rel: "start", Href: "/opds", Type: mediaTypeNavigation},
		{Rel: "search", Href: "/opds/search.xml", Type: "application/opensearchdescription+xml"},
	}
}

// root handles GET /opds — the top-level OPDS navigation feed.
func (h *Handler) root(w http.ResponseWriter, r *http.Request) {
	if wantsJSON(r) {
		writeJSONFeed(w, r, feed2{
			Metadata: metadata2{Title: "discodrive Library"},
			Links: []link2{
				{Rel: "self", Href: "/opds", Type: mediaTypeOPDS2},
				{Rel: "search", Href: "/opds/search{?query}", Type: mediaTypeOPDS2, Templated: true},
			},
			Navigation: []link2{
				{Href: "/opds/new", Title: "New", Type: mediaTypeOPDS2},
				{Href: "/opds/nav/authors", Title: "By Author", Type: mediaTypeOPDS2},
				{Href: "/opds/nav/series", Title: "By Series", Type: mediaTypeOPDS2},
				{Href: "/opds/nav/genres", Title: "By Genre", Type: mediaTypeOPDS2},
				{Href: "/opds/all", Title: "All Books", Type: mediaTypeOPDS2},
			},
		})
		return
	}

	updated := now()
	feed := atomFeed{
		ID:      "urn:discodrive:opds:root",
		Title:   "discodrive OPDS Catalog",
		Updated: updated,
		Links:   selfStartLinks("/opds"),
		Entries: []atomEntry{
			{
				ID:      "urn:discodrive:opds:new",
				Title:   "New",
				Updated: updated,
				Content: &atomContent{Type: "text", Text: "Recently added books"},
				Links: []atomLink{
					{Rel: "subsection", Href: "/opds/new", Type: mediaTypeAcquisition},
				},
			},
			{
				ID:      "urn:discodrive:opds:authors",
				Title:   "By Author",
				Updated: updated,
				Content: &atomContent{Type: "text", Text: "Browse books by author"},
				Links: []atomLink{
					{Rel: "subsection", Href: "/opds/nav/authors", Type: mediaTypeNavigation},
				},
			},
			{
				ID:      "urn:discodrive:opds:series",
				Title:   "By Series",
				Updated: updated,
				Content: &atomContent{Type: "text", Text: "Browse books by series"},
				Links: []atomLink{
					{Rel: "subsection", Href: "/opds/nav/series", Type: mediaTypeNavigation},
				},
			},
			{
				ID:      "urn:discodrive:opds:genres",
				Title:   "By Genre",
				Updated: updated,
				Content: &atomContent{Type: "text", Text: "Browse books by genre"},
				Links: []atomLink{
					{Rel: "subsection", Href: "/opds/nav/genres", Type: mediaTypeNavigation},
				},
			},
			{
				ID:      "urn:discodrive:opds:all",
				Title:   "All Books",
				Updated: updated,
				Content: &atomContent{Type: "text", Text: "All accessible books"},
				Links: []atomLink{
					{Rel: "subsection", Href: "/opds/all", Type: mediaTypeAcquisition},
				},
			},
		},
	}
	writeAtomFeed(w, r, mediaTypeNavigation, feed)
}

// navAuthors handles GET /opds/nav/authors — one entry per accessible author.
func (h *Handler) navAuthors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, err := db.ParseUUID(userIDFromContext(ctx))
	if err != nil {
		http.Error(w, "opds: bad user id", http.StatusInternalServerError)
		return
	}

	authors, err := h.q.AccessibleAuthors(ctx, userID)
	if err != nil {
		http.Error(w, "opds: query failed", http.StatusInternalServerError)
		return
	}

	if wantsJSON(r) {
		navLinks := make([]link2, 0, len(authors))
		for _, a := range authors {
			navLinks = append(navLinks, link2{
				Href:  "/opds/author/" + encodeNameID(a.Name),
				Title: a.Name,
				Type:  mediaTypeOPDS2,
			})
		}
		writeJSONFeed(w, r, feed2{
			Metadata:   metadata2{Title: "Authors"},
			Links:      []link2{{Rel: "self", Href: "/opds/nav/authors", Type: mediaTypeOPDS2}},
			Navigation: navLinks,
		})
		return
	}

	updated := now()
	entries := make([]atomEntry, 0, len(authors))
	for _, a := range authors {
		escaped := encodeNameID(a.Name)
		entries = append(entries, atomEntry{
			ID:      "urn:discodrive:opds:author:" + escaped,
			Title:   a.Name,
			Updated: updated,
			Links: []atomLink{
				{Rel: "subsection", Href: "/opds/author/" + escaped, Type: mediaTypeAcquisition},
			},
		})
	}

	feed := atomFeed{
		ID:      "urn:discodrive:opds:nav:authors",
		Title:   "Authors",
		Updated: updated,
		Links:   selfStartLinks("/opds/nav/authors"),
		Entries: entries,
	}
	writeAtomFeed(w, r, mediaTypeNavigation, feed)
}

// navSeries handles GET /opds/nav/series — one entry per accessible series.
func (h *Handler) navSeries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, err := db.ParseUUID(userIDFromContext(ctx))
	if err != nil {
		http.Error(w, "opds: bad user id", http.StatusInternalServerError)
		return
	}

	seriesList, err := h.q.AccessibleSeries(ctx, userID)
	if err != nil {
		http.Error(w, "opds: query failed", http.StatusInternalServerError)
		return
	}

	if wantsJSON(r) {
		navLinks := make([]link2, 0, len(seriesList))
		for _, s := range seriesList {
			if !s.Valid {
				continue
			}
			navLinks = append(navLinks, link2{
				Href:  "/opds/series/" + encodeNameID(s.String),
				Title: s.String,
				Type:  mediaTypeOPDS2,
			})
		}
		writeJSONFeed(w, r, feed2{
			Metadata:   metadata2{Title: "Series"},
			Links:      []link2{{Rel: "self", Href: "/opds/nav/series", Type: mediaTypeOPDS2}},
			Navigation: navLinks,
		})
		return
	}

	updated := now()
	entries := make([]atomEntry, 0, len(seriesList))
	for _, s := range seriesList {
		if !s.Valid {
			continue
		}
		escaped := encodeNameID(s.String)
		entries = append(entries, atomEntry{
			ID:      "urn:discodrive:opds:series:" + escaped,
			Title:   s.String,
			Updated: updated,
			Links: []atomLink{
				{Rel: "subsection", Href: "/opds/series/" + escaped, Type: mediaTypeAcquisition},
			},
		})
	}

	feed := atomFeed{
		ID:      "urn:discodrive:opds:nav:series",
		Title:   "Series",
		Updated: updated,
		Links:   selfStartLinks("/opds/nav/series"),
		Entries: entries,
	}
	writeAtomFeed(w, r, mediaTypeNavigation, feed)
}

// navGenres handles GET /opds/nav/genres — one entry per accessible genre/tag.
func (h *Handler) navGenres(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, err := db.ParseUUID(userIDFromContext(ctx))
	if err != nil {
		http.Error(w, "opds: bad user id", http.StatusInternalServerError)
		return
	}

	genres, err := h.q.AccessibleBookGenres(ctx, userID)
	if err != nil {
		http.Error(w, "opds: query failed", http.StatusInternalServerError)
		return
	}

	if wantsJSON(r) {
		navLinks := make([]link2, 0, len(genres))
		for _, g := range genres {
			navLinks = append(navLinks, link2{
				Href:  "/opds/genre/" + encodeNameID(g),
				Title: g,
				Type:  mediaTypeOPDS2,
			})
		}
		writeJSONFeed(w, r, feed2{
			Metadata:   metadata2{Title: "Genres"},
			Links:      []link2{{Rel: "self", Href: "/opds/nav/genres", Type: mediaTypeOPDS2}},
			Navigation: navLinks,
		})
		return
	}

	updated := now()
	entries := make([]atomEntry, 0, len(genres))
	for _, g := range genres {
		escaped := encodeNameID(g)
		entries = append(entries, atomEntry{
			ID:      "urn:discodrive:opds:genre:" + escaped,
			Title:   g,
			Updated: updated,
			Links: []atomLink{
				{Rel: "subsection", Href: "/opds/genre/" + escaped, Type: mediaTypeAcquisition},
			},
		})
	}

	feed := atomFeed{
		ID:      "urn:discodrive:opds:nav:genres",
		Title:   "Genres",
		Updated: updated,
		Links:   selfStartLinks("/opds/nav/genres"),
		Entries: entries,
	}
	writeAtomFeed(w, r, mediaTypeNavigation, feed)
}
