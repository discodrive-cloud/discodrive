package opds

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"github.com/jackc/pgx/v5"

	"discodrive/internal/coverthumb"
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
	h.serveCover(w, r, bookIDStr, 0)
}

// coverThumbnail handles GET /opds/cover/{bookId}/thumbnail.
//
// The cover scaled down to thumbnailSide, cached on disk; a cover that cannot be scaled
// (not decodable, too large) is served whole.
func (h *Handler) coverThumbnail(w http.ResponseWriter, r *http.Request) {
	bookIDStr := r.PathValue("bookId")
	h.serveCover(w, r, bookIDStr, thumbnailSide)
}

// thumbnailSide is the longest side of an OPDS cover thumbnail (a catalog row).
const thumbnailSide = 320

// serveCover is the shared implementation for both cover and coverThumbnail.
// It parses bookID, gate-checks accessibility, resolves the cover_path from the
// DB, opens the file, and serves it via http.ServeContent.
func (h *Handler) serveCover(w http.ResponseWriter, r *http.Request, bookIDStr string, side int) {
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

	if side != 0 {
		// The cover file has no content hash on record: name the picture by what
		// identifies this version of it.
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", book.CoverPath.String, fi.Size(), fi.ModTime().UnixNano())))
		thumb, ok := coverthumb.Get(h.storageRoot, hex.EncodeToString(sum[:]), side, func() ([]byte, bool) {
			data, err := io.ReadAll(io.LimitReader(f, 8<<20))
			return data, err == nil
		})
		if ok {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Type", "image/jpeg")
			http.ServeContent(w, r, "thumbnail.jpg", fi.ModTime(), bytes.NewReader(thumb))
			return
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// http.ServeContent sniffs Content-Type from the file extension/content and
	// handles Range, If-Modified-Since, and ETag automatically.
	http.ServeContent(w, r, filepath.Base(coverPath), fi.ModTime(), f)
}
