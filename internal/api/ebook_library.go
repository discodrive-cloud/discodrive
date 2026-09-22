package api

import (
	"errors"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/storage"
)

const ebookLibraryPageSize = 50

// ebookBookDTO is the JSON representation of a book in the library list.
type ebookBookDTO struct {
	ID          string   `json:"id"`
	NodeID      string   `json:"nodeId"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors"`
	Series      string   `json:"series,omitempty"`
	SeriesIndex float32  `json:"seriesIndex,omitempty"`
	Language    string   `json:"language,omitempty"`
	Format      string   `json:"format"`
	Tags        []string `json:"tags"`
	HasCover    bool     `json:"hasCover"`
}

// ebookListResponse wraps a book list with total count.
type ebookListResponse struct {
	Books []ebookBookDTO `json:"books"`
	Total int64          `json:"total"`
}

// ebookFacetsResponse contains the distinct facet values for the current user.
type ebookFacetsResponse struct {
	Authors []string `json:"authors"`
	Series  []string `json:"series"`
	Genres  []string `json:"genres"`
}

// bookToDTO converts a db.Book to ebookBookDTO, fetching authors and tags.
func (s *Server) bookToDTO(r *http.Request, book db.Book) ebookBookDTO {
	ctx := r.Context()
	dto := ebookBookDTO{
		ID:       db.UUIDString(book.ID),
		NodeID:   db.UUIDString(book.NodeID),
		Title:    book.Title,
		Format:   book.Format,
		HasCover: book.CoverPath.Valid && book.CoverPath.String != "",
	}
	if book.Language.Valid {
		dto.Language = book.Language.String
	}
	if book.Series.Valid {
		dto.Series = book.Series.String
	}
	if book.SeriesIndex.Valid {
		dto.SeriesIndex = book.SeriesIndex.Float32
	}

	// Fetch authors — best-effort; empty slice on error.
	authors, err := s.q.BookAuthors(ctx, book.ID)
	if err == nil {
		dto.Authors = make([]string, 0, len(authors))
		for _, a := range authors {
			dto.Authors = append(dto.Authors, a.Name)
		}
	} else {
		dto.Authors = []string{}
	}

	// Fetch tags — best-effort.
	tags, err := s.q.BookTags(ctx, book.ID)
	if err == nil {
		dto.Tags = tags
	} else {
		dto.Tags = []string{}
	}
	if dto.Authors == nil {
		dto.Authors = []string{}
	}
	if dto.Tags == nil {
		dto.Tags = []string{}
	}
	return dto
}

// handleListEbooks handles GET /me/ebooks/library.
//
// Query params:
//   - q      — full-text search (title/author/series)
//   - author — filter by author name
//   - series — filter by series name
//   - genre  — filter by genre/tag
//   - start  — pagination offset (default 0)
//
// Security: all queries are scoped by the SESSION userID from auth.UserID(ctx),
// so only the user's own and shared books are returned.
func (s *Server) handleListEbooks(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}

	q := r.URL.Query()
	search := q.Get("q")
	author := q.Get("author")
	series := q.Get("series")
	genre := q.Get("genre")
	start, _ := strconv.Atoi(q.Get("start"))
	if start < 0 {
		start = 0
	}
	limit := int32(ebookLibraryPageSize)
	offset := int32(start)

	ctx := r.Context()
	var books []db.Book

	switch {
	case search != "":
		books, err = s.q.SearchAccessibleBooks(ctx, db.SearchAccessibleBooksParams{
			UserID:  uid,
			Column2: pgtype.Text{String: search, Valid: true},
			Limit:   limit,
			Offset:  offset,
		})
	case author != "":
		books, err = s.q.AccessibleBooksByAuthor(ctx, db.AccessibleBooksByAuthorParams{
			UserID: uid,
			Name:   author,
			Limit:  limit,
			Offset: offset,
		})
	case series != "":
		books, err = s.q.AccessibleBooksBySeries(ctx, db.AccessibleBooksBySeriesParams{
			UserID: uid,
			Series: pgtype.Text{String: series, Valid: true},
			Limit:  limit,
			Offset: offset,
		})
	case genre != "":
		books, err = s.q.AccessibleBooksByGenre(ctx, db.AccessibleBooksByGenreParams{
			UserID: uid,
			Tag:    genre,
			Limit:  limit,
			Offset: offset,
		})
	default:
		books, err = s.q.AccessibleBooksAll(ctx, db.AccessibleBooksAllParams{
			UserID: uid,
			Limit:  limit,
			Offset: offset,
		})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Total count — only for the unfiltered case; for filtered results use len.
	var total int64
	if search == "" && author == "" && series == "" && genre == "" {
		total, err = s.q.CountAccessibleBooks(ctx, uid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	} else {
		total = int64(len(books))
	}

	dtos := make([]ebookBookDTO, 0, len(books))
	for _, b := range books {
		dtos = append(dtos, s.bookToDTO(r, b))
	}

	writeJSON(w, http.StatusOK, ebookListResponse{Books: dtos, Total: total})
}

// handleEbookFacets handles GET /me/ebooks/library/facets.
//
// Returns the distinct authors, series, and genres for the current user's
// accessible books. Used by the web UI to populate filter dropdowns.
// Security: all queries scoped by the SESSION userID.
func (s *Server) handleEbookFacets(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	ctx := r.Context()

	authorRows, err := s.q.AccessibleAuthors(ctx, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	authors := make([]string, 0, len(authorRows))
	for _, a := range authorRows {
		authors = append(authors, a.Name)
	}

	seriesRows, err := s.q.AccessibleSeries(ctx, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	seriesList := make([]string, 0, len(seriesRows))
	for _, s := range seriesRows {
		if s.Valid {
			seriesList = append(seriesList, s.String)
		}
	}

	genres, err := s.q.AccessibleBookGenres(ctx, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if genres == nil {
		genres = []string{}
	}

	writeJSON(w, http.StatusOK, ebookFacetsResponse{
		Authors: authors,
		Series:  seriesList,
		Genres:  genres,
	})
}

// handleGetEbookCover handles GET /me/ebooks/library/{id}/cover.
//
// Serves the cached cover image for a book. Mirrors handleGetPodcastCover.
// Security: AccessibleBook scopes by userID — any unknown, inaccessible, or
// cover-less book returns 404 without distinction.
func (s *Server) handleGetEbookCover(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	book, err := s.q.AccessibleBook(r.Context(), db.AccessibleBookParams{
		UserID: uid,
		ID:     id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if !book.CoverPath.Valid || book.CoverPath.String == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// Only DB-sourced cover_path is joined with storageRoot — never a client value.
	path := filepath.Join(s.storageRoot, book.CoverPath.String)
	f, err := storage.NewLocalDisk(s.storageRoot).Open(book.CoverPath.String)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	ct := mime.TypeByExtension(filepath.Ext(path))
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// handleDownloadEbook handles GET /me/ebooks/library/{id}/download.
//
// Resolves the book (scoped to the session user via AccessibleBook), fetches
// the underlying node, and serves the original file with Range support.
// Security: any unknown or inaccessible book returns 404.
func (s *Server) handleDownloadEbook(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	ctx := r.Context()
	book, err := s.q.AccessibleBook(ctx, db.AccessibleBookParams{
		UserID: uid,
		ID:     id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	node, err := s.q.GetNode(ctx, book.NodeID)
	if err != nil || !node.DiskPath.Valid {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// Use the same delivery path as streamFile, but with the book's contentType.
	f, err := storage.NewLocalDisk(s.storageRoot).Open(node.DiskPath.String)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if book.ContentType != "" {
		w.Header().Set("Content-Type", book.ContentType)
	}
	http.ServeContent(w, r, node.Name, fi.ModTime(), f)
}
