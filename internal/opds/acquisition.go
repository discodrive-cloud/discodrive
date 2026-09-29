package opds

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"discodrive/internal/db"

	"github.com/jackc/pgx/v5/pgtype"
)

// appendStart adds the pagination start offset to a feed URL, choosing
// '?' or '&' depending on whether base already has a query string.
func appendStart(base string, offset int) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "start=" + strconv.Itoa(offset)
}

// pageSize is the number of books returned per acquisition feed page.
// It is a package-level var (not a const) so tests can lower it.
var pageSize = 50

// listBooks renders a slice of book rows as an OPDS acquisition feed, choosing
// between OPDS 2.0 JSON and OPDS 1.2 Atom based on the request's Accept header.
//
// total/offset describe the position of this page within the full result set so
// a rel="next" link can be emitted when more rows remain. self is the feed's own
// path (without the ?start= query); offset is appended to build the next link.
//
// NOTE on N+1: the caller fetches authors and tags per book (one query each),
// so a page of N books issues 2N extra queries. Acceptable at pageSize=50 for
// this opt-in catalog; a JOIN-based batch fetch can replace it if it ever matters.
func (h *Handler) listBooks(w http.ResponseWriter, r *http.Request, books []db.Book, title, self string, total int64, offset int) {
	ctx := r.Context()

	// Resolve per-book authors. Tags are fetched too but only used for
	// completeness of the acquisition rendering (Atom carries no tag here).
	authorsByBook := make([][]string, len(books))
	for i, b := range books {
		rows, err := h.q.BookAuthors(ctx, b.ID)
		if err != nil {
			http.Error(w, "opds: query failed", http.StatusInternalServerError)
			return
		}
		names := make([]string, 0, len(rows))
		for _, a := range rows {
			names = append(names, a.Name)
		}
		authorsByBook[i] = names

		// BookTags is part of the consumed interface; fetched to surface query
		// errors early even though acquisition entries don't render tags.
		if _, err := h.q.BookTags(ctx, b.ID); err != nil {
			http.Error(w, "opds: query failed", http.StatusInternalServerError)
			return
		}
	}

	hasNext := total > int64(offset+len(books))
	nextOffset := offset + len(books)

	if wantsJSON(r) {
		pubs := make([]publication2, 0, len(books))
		for i, b := range books {
			pubs = append(pubs, bookPublication2(b, authorsByBook[i]))
		}

		links := []link2{
			{Rel: "self", Href: self, Type: mediaTypeOPDS2},
			{Rel: "start", Href: "/opds", Type: mediaTypeOPDS2},
		}
		if hasNext {
			links = append(links, link2{Rel: "next", Href: appendStart(self, nextOffset), Type: mediaTypeOPDS2})
		}

		writeJSONFeed(w, feed2{
			Metadata:     metadata2{Title: title},
			Links:        links,
			Publications: pubs,
		})
		return
	}

	updated := now()
	entries := make([]atomEntry, 0, len(books))
	for i, b := range books {
		entries = append(entries, bookEntry(b, authorsByBook[i]))
	}

	links := []atomLink{
		{Rel: "self", Href: self, Type: mediaTypeAcquisition},
		{Rel: "start", Href: "/opds", Type: mediaTypeNavigation},
		{Rel: "up", Href: "/opds", Type: mediaTypeNavigation},
	}
	if hasNext {
		links = append(links, atomLink{Rel: "next", Href: appendStart(self, nextOffset), Type: mediaTypeAcquisition})
	}

	writeAtomFeed(w, mediaTypeAcquisition, atomFeed{
		ID:      "urn:discodrive:opds:acquisition",
		Title:   title,
		Updated: updated,
		Links:   links,
		Entries: entries,
	})
}

// bookEntry builds an OPDS 1.2 Atom acquisition entry for a single book.
func bookEntry(b db.Book, authors []string) atomEntry {
	id := db.UUIDString(b.ID)

	updated := time.Now().UTC().Format(time.RFC3339)
	if b.AddedAt.Valid {
		updated = b.AddedAt.Time.UTC().Format(time.RFC3339)
	}

	desc := ""
	if b.Description.Valid {
		desc = b.Description.String
	}

	atomAuthors := make([]atomAuthor, 0, len(authors))
	for _, name := range authors {
		atomAuthors = append(atomAuthors, atomAuthor{Name: name})
	}

	links := []atomLink{
		{Rel: "http://opds-spec.org/acquisition", Href: "/opds/download/" + id, Type: b.ContentType},
	}
	if b.CoverPath.Valid && b.CoverPath.String != "" {
		links = append(links,
			atomLink{Rel: "http://opds-spec.org/image", Href: "/opds/cover/" + id, Type: "image/jpeg"},
			atomLink{Rel: "http://opds-spec.org/image/thumbnail", Href: "/opds/cover/" + id + "/thumbnail", Type: "image/jpeg"},
		)
	}

	return atomEntry{
		ID:      "urn:discodrive:book:" + id,
		Title:   b.Title,
		Updated: updated,
		Content: &atomContent{Type: "text", Text: desc},
		Authors: atomAuthors,
		Links:   links,
	}
}

// bookPublication2 builds an OPDS 2.0 publication object for a single book.
func bookPublication2(b db.Book, authors []string) publication2 {
	id := db.UUIDString(b.ID)

	pubAuthors := make([]pubAuthor2, 0, len(authors))
	for _, name := range authors {
		pubAuthors = append(pubAuthors, pubAuthor2{Name: name})
	}

	lang := ""
	if b.Language.Valid {
		lang = b.Language.String
	}

	pub := publication2{
		Metadata: pubMetadata2{
			Title:      b.Title,
			Author:     pubAuthors,
			Language:   lang,
			Identifier: "urn:discodrive:book:" + id,
		},
		Links: []link2{
			{Rel: "http://opds-spec.org/acquisition", Href: "/opds/download/" + id, Type: b.ContentType},
		},
	}
	if b.CoverPath.Valid && b.CoverPath.String != "" {
		pub.Images = []link2{
			{Href: "/opds/cover/" + id, Type: "image/jpeg"},
			{Href: "/opds/cover/" + id + "/thumbnail", Type: "image/jpeg"},
		}
	}
	return pub
}

// maxOffset keeps ?start= (and start+pageSize) inside the int32 query argument.
const maxOffset = 1 << 30

// parseOffset reads the ?start= query parameter, defaulting to 0 on absence or
// error and clamped to maxOffset.
func parseOffset(r *http.Request) int {
	v := r.URL.Query().Get("start")
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(v, "-") {
		return maxOffset
	}
	if err != nil || n < 0 {
		return 0
	}
	return int(min(n, maxOffset))
}

// acqNew handles GET /opds/new — recently added accessible books (not paginated
// beyond a single page; "newest" is a bounded list by nature).
func (h *Handler) acqNew(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, err := db.ParseUUID(userIDFromContext(ctx))
	if err != nil {
		http.Error(w, "opds: bad user id", http.StatusInternalServerError)
		return
	}

	offset := parseOffset(r)
	books, err := h.q.AccessibleBooksNewest(ctx, db.AccessibleBooksNewestParams{
		UserID: userID,
		Limit:  int32(pageSize),
		Offset: int32(offset),
	})
	if err != nil {
		http.Error(w, "opds: query failed", http.StatusInternalServerError)
		return
	}

	// Total is unknown/unbounded for "newest"; use offset+len so a full page
	// still advertises a next link.
	total := int64(offset + len(books))
	if len(books) == pageSize {
		total++ // hint that another page may exist
	}
	h.listBooks(w, r, books, "New", "/opds/new", total, offset)
}

// acqAll handles GET /opds/all — all accessible books with pagination + rel=next.
func (h *Handler) acqAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, err := db.ParseUUID(userIDFromContext(ctx))
	if err != nil {
		http.Error(w, "opds: bad user id", http.StatusInternalServerError)
		return
	}

	offset := parseOffset(r)
	total, err := h.q.CountAccessibleBooks(ctx, userID)
	if err != nil {
		http.Error(w, "opds: query failed", http.StatusInternalServerError)
		return
	}
	books, err := h.q.AccessibleBooksAll(ctx, db.AccessibleBooksAllParams{
		UserID: userID,
		Limit:  int32(pageSize),
		Offset: int32(offset),
	})
	if err != nil {
		http.Error(w, "opds: query failed", http.StatusInternalServerError)
		return
	}
	h.listBooks(w, r, books, "All Books", "/opds/all", total, offset)
}

// acqAuthor handles GET /opds/author/{id} — accessible books by a given author.
func (h *Handler) acqAuthor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, err := db.ParseUUID(userIDFromContext(ctx))
	if err != nil {
		http.Error(w, "opds: bad user id", http.StatusInternalServerError)
		return
	}

	name := decodeNameID(r.PathValue("id"))
	offset := parseOffset(r)
	books, err := h.q.AccessibleBooksByAuthor(ctx, db.AccessibleBooksByAuthorParams{
		UserID: userID,
		Name:   name,
		Limit:  int32(pageSize),
		Offset: int32(offset),
	})
	if err != nil {
		http.Error(w, "opds: query failed", http.StatusInternalServerError)
		return
	}
	total := int64(offset + len(books))
	if len(books) == pageSize {
		total++
	}
	h.listBooks(w, r, books, name, "/opds/author/"+encodeNameID(name), total, offset)
}

// acqSeries handles GET /opds/series/{id} — accessible books in a given series.
func (h *Handler) acqSeries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, err := db.ParseUUID(userIDFromContext(ctx))
	if err != nil {
		http.Error(w, "opds: bad user id", http.StatusInternalServerError)
		return
	}

	name := decodeNameID(r.PathValue("id"))
	offset := parseOffset(r)
	books, err := h.q.AccessibleBooksBySeries(ctx, db.AccessibleBooksBySeriesParams{
		UserID: userID,
		Series: pgtype.Text{String: name, Valid: true},
		Limit:  int32(pageSize),
		Offset: int32(offset),
	})
	if err != nil {
		http.Error(w, "opds: query failed", http.StatusInternalServerError)
		return
	}
	total := int64(offset + len(books))
	if len(books) == pageSize {
		total++
	}
	h.listBooks(w, r, books, name, "/opds/series/"+encodeNameID(name), total, offset)
}

// acqGenre handles GET /opds/genre/{tag} — accessible books for a given genre/tag.
func (h *Handler) acqGenre(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, err := db.ParseUUID(userIDFromContext(ctx))
	if err != nil {
		http.Error(w, "opds: bad user id", http.StatusInternalServerError)
		return
	}

	tag := decodeNameID(r.PathValue("tag"))
	offset := parseOffset(r)
	books, err := h.q.AccessibleBooksByGenre(ctx, db.AccessibleBooksByGenreParams{
		UserID: userID,
		Tag:    tag,
		Limit:  int32(pageSize),
		Offset: int32(offset),
	})
	if err != nil {
		http.Error(w, "opds: query failed", http.StatusInternalServerError)
		return
	}
	total := int64(offset + len(books))
	if len(books) == pageSize {
		total++
	}
	h.listBooks(w, r, books, tag, "/opds/genre/"+encodeNameID(tag), total, offset)
}
