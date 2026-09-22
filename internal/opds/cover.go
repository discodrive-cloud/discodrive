package opds

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/jackc/pgx/v5"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// cover handles GET /opds/cover/{bookId}.
//
// It resolves the book (scoped to the authenticated user via AccessibleBook),
// retrieves the cached cover file from storageRoot, and streams it with full
// HTTP Range support via http.ServeContent.
//
// Security: any unknown, inaccessible, or cover-less book returns 404 — no
// distinction between cases to avoid information leakage. Only the cover_path
// value from the DB (never a client-supplied value) is joined with storageRoot.
func (h *Handler) cover(w http.ResponseWriter, r *http.Request) {
	bookIDStr := r.PathValue("bookId")
	h.serveCover(w, r, bookIDStr)
}

// coverThumbnail handles GET /opds/cover/{bookId}/thumbnail.
//
// v1: returns the same image as the full cover — no resizing is performed.
// A real thumbnail resize (e.g. via golang.org/x/image) is a deferred follow-up
// outside the current implementation scope.
func (h *Handler) coverThumbnail(w http.ResponseWriter, r *http.Request) {
	bookIDStr := r.PathValue("bookId")
	h.serveCover(w, r, bookIDStr)
}

// serveCover is the shared implementation for both cover and coverThumbnail.
// It parses bookID, gate-checks accessibility, resolves the cover_path from the
// DB, opens the file, and serves it via http.ServeContent.
func (h *Handler) serveCover(w http.ResponseWriter, r *http.Request, bookIDStr string) {
	bookUUID, err := db.ParseUUID(bookIDStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	userIDStr := userIDFromContext(r.Context())
	userUUID, err := db.ParseUUID(userIDStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()

	book, err := h.q.AccessibleBook(ctx, db.AccessibleBookParams{
		UserID: userUUID,
		ID:     bookUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if !book.CoverPath.Valid || book.CoverPath.String == "" {
		http.NotFound(w, r)
		return
	}

	// Only the DB-sourced cover_path is joined with storageRoot — never a client value.
	coverPath := filepath.Join(h.storageRoot, book.CoverPath.String)
	f, err := storage.NewLocalDisk(h.storageRoot).Open(book.CoverPath.String)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// http.ServeContent sniffs Content-Type from the file extension/content and
	// handles Range, If-Modified-Since, and ETag automatically.
	http.ServeContent(w, r, filepath.Base(coverPath), fi.ModTime(), f)
}
