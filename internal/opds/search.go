package opds

import (
	"encoding/xml"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// openSearchDescription is the root element of an OpenSearch 1.1 description document.
type openSearchDescription struct {
	XMLName     xml.Name        `xml:"OpenSearchDescription"`
	Xmlns       string          `xml:"xmlns,attr"`
	ShortName   string          `xml:"ShortName"`
	Description string          `xml:"Description"`
	Urls        []openSearchURL `xml:"Url"`
}

// openSearchURL is a single search endpoint descriptor inside an OpenSearch document.
type openSearchURL struct {
	Type     string `xml:"type,attr"`
	Template string `xml:"template,attr"`
}

// searchDescription handles GET /opds/search.xml — returns the OpenSearch 1.1
// description document so clients can discover the search endpoint and its
// supported response formats.
func (h *Handler) searchDescription(w http.ResponseWriter, r *http.Request) {
	doc := openSearchDescription{
		Xmlns:       "http://a9.com/-/spec/opensearch/1.1/",
		ShortName:   "discodrive",
		Description: "Search the discodrive ebook catalog",
		Urls: []openSearchURL{
			{
				Type:     mediaTypeAcquisition,
				Template: "/opds/search?q={searchTerms}",
			},
			{
				Type:     mediaTypeOPDS2,
				Template: "/opds/search?q={searchTerms}",
			},
		},
	}

	data, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		http.Error(w, "opds: marshal failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/opensearchdescription+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	w.Write(data)
}

// searchParam extracts the search query from the request, checking q first,
// then query, then searchTerms (clients use different param names).
func searchParam(r *http.Request) string {
	q := r.URL.Query()
	for _, name := range []string{"q", "query", "searchTerms"} {
		if v := q.Get(name); v != "" {
			return v
		}
	}
	return ""
}

// search handles GET /opds/search — returns an acquisition feed of books whose
// title, author name, or series matches the search term. An empty query returns
// an empty feed (200, not an error). Results are always scoped to the
// authenticated user's accessible books.
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	q := searchParam(r)
	if q == "" {
		// Empty query → empty acquisition feed; not an error.
		h.listBooks(w, r, nil, "Search Results", "/opds/search", 0, 0)
		return
	}

	userID, err := db.ParseUUID(userIDFromContext(ctx))
	if err != nil {
		http.Error(w, "opds: bad user id", http.StatusInternalServerError)
		return
	}

	offset := parseOffset(r)
	books, err := h.q.SearchAccessibleBooks(ctx, db.SearchAccessibleBooksParams{
		UserID:  userID,
		Column2: pgtype.Text{String: db.EscapeLike(q), Valid: true},
		Limit:   int32(pageSize),
		Offset:  int32(offset),
	})
	if err != nil {
		http.Error(w, "opds: query failed", http.StatusInternalServerError)
		return
	}

	// Use offset+len as total and hint at another page when a full page returned,
	// matching the pattern used by acqNew / acqAuthor / acqSeries / acqGenre.
	total := int64(offset + len(books))
	if len(books) == pageSize {
		total++
	}

	// Build a self href that preserves the search term, with the term escaped
	// so special characters (e.g. '&', '+', spaces) don't produce invalid URLs.
	self := "/opds/search?q=" + url.QueryEscape(q)

	h.listBooks(w, r, books, "Search Results", self, total, offset)
}
