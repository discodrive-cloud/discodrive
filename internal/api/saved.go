package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/fetchguard"
	"discodrive/internal/saved"
	"discodrive/internal/secret"
	"discodrive/internal/storage"
)

// maxSavedURLLen keeps (user_id, url, kind) well under the btree index tuple limit.
const maxSavedURLLen = 2000

// maxSavedContentHTMLLen caps the client-extracted article HTML (2 MiB).
const maxSavedContentHTMLLen = 2 << 20

// maxSavedCookieLen caps the forwarded browser Cookie header (16 KiB).
const maxSavedCookieLen = 16 << 10

type savedItemDTO struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	SizeBytes  *int64 `json:"size_bytes"`
	BytesDone  int64  `json:"bytes_done"`
	HasContent bool   `json:"has_content"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func toSavedItemDTO(it db.SavedItem) savedItemDTO {
	d := savedItemDTO{
		ID:         db.UUIDString(it.ID),
		URL:        it.Url,
		Kind:       it.Kind,
		Title:      it.Title,
		Status:     it.Status,
		Error:      it.ErrorMsg,
		BytesDone:  it.BytesDone,
		HasContent: it.Kind == saved.KindArticle && it.Status == saved.StatusDone && it.ContentPath.Valid,
		CreatedAt:  it.CreatedAt.Time.Format(time.RFC3339),
		UpdatedAt:  it.UpdatedAt.Time.Format(time.RFC3339),
	}
	if it.SizeBytes.Valid {
		s := it.SizeBytes.Int64
		d.SizeBytes = &s
	}
	return d
}

func validSavedKind(k string) bool {
	return k == saved.KindArticle || k == saved.KindDownload
}

func validSavedStatus(st string) bool {
	return st == saved.StatusPending || st == saved.StatusProcessing ||
		st == saved.StatusDone || st == saved.StatusError
}

// POST /me/saved — upsert a saved item and kick off processing if it is new.
func (s *Server) handleSavedCreate(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	var req struct {
		URL         string `json:"url"`
		Kind        string `json:"kind"`
		Title       string `json:"title"`
		ContentHTML string `json:"content_html"`
		Cookie      string `json:"cookie"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !validSavedKind(req.Kind) {
		writeError(w, http.StatusBadRequest, "kind must be article or download")
		return
	}
	if req.URL == "" || len(req.URL) > maxSavedURLLen {
		writeError(w, http.StatusBadRequest, "url is required and must be at most 2000 characters")
		return
	}
	if req.ContentHTML != "" && req.Kind != saved.KindArticle {
		writeError(w, http.StatusBadRequest, "content_html is only valid for articles")
		return
	}
	if len(req.ContentHTML) > maxSavedContentHTMLLen {
		writeError(w, http.StatusBadRequest, "content_html is too large")
		return
	}
	if req.Cookie != "" && req.Kind != saved.KindDownload {
		writeError(w, http.StatusBadRequest, "cookie is only valid for downloads")
		return
	}
	if len(req.Cookie) > maxSavedCookieLen {
		writeError(w, http.StatusBadRequest, "cookie is too large")
		return
	}
	// With client-supplied content the server never fetches the URL, so the
	// SSRF guard has nothing to protect: addresses behind a paywall or a login
	// are only ever resolved on the client.
	if req.ContentHTML == "" {
		if err := s.saved.Validate(req.URL); err != nil {
			writeError(w, http.StatusBadRequest, "url is not allowed: "+err.Error())
			return
		}
	}
	item, err := s.saved.Create(r.Context(), uid, req.URL, req.Kind, req.Title, req.ContentHTML, req.Cookie)
	if errors.Is(err, secret.ErrNoKey) || errors.Is(err, fetchguard.ErrBlocked) {
		writeError(w, http.StatusBadRequest, "Authenticated downloads require HTTPS and configured secret encryption")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toSavedItemDTO(item))
}

// GET /me/saved?kind=&status=&q=&limit=&offset= — list saved items, newest first.
func (s *Server) handleSavedList(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	kind := r.URL.Query().Get("kind")
	// Downloads are an internal queue — they can be polled by id, never listed.
	if kind != "" && kind != saved.KindArticle {
		writeError(w, http.StatusBadRequest, "invalid kind")
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && !validSavedStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	limit := int32(200)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = int32(n)
		}
	}
	var offset int32
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = int32(n)
		}
	}
	items, err := s.q.ListSavedItems(r.Context(), db.ListSavedItemsParams{
		UserID: uid,
		Kind:   kind,
		Status: status,
		Q:      r.URL.Query().Get("q"),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	dtos := make([]savedItemDTO, 0, len(items))
	for _, it := range items {
		dtos = append(dtos, toSavedItemDTO(it))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// GET /me/saved/{id} — a single saved item (used by the article reader).
func (s *Server) handleSavedGet(w http.ResponseWriter, r *http.Request) {
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
	item, err := s.q.GetSavedItemForUser(r.Context(), db.GetSavedItemForUserParams{ID: id, UserID: uid})
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, toSavedItemDTO(item))
}

// POST /me/saved/{id}/retry — re-queue an error/done item and kick it off.
func (s *Server) handleSavedRetry(w http.ResponseWriter, r *http.Request) {
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
	n, err := s.q.RetrySavedItem(r.Context(), db.RetrySavedItemParams{ID: id, UserID: uid})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		// Unknown id, someone else's item, or a pending/processing one.
		writeError(w, http.StatusConflict, "item is not retryable")
		return
	}
	item, err := s.q.GetSavedItemForUser(r.Context(), db.GetSavedItemForUserParams{ID: id, UserID: uid})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.saved.Kickoff(r.Context(), item)
	writeJSON(w, http.StatusOK, toSavedItemDTO(item))
}

// GET /me/saved/{id}/content — serve the stored article markdown.
func (s *Server) handleSavedContent(w http.ResponseWriter, r *http.Request) {
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
	item, err := s.q.GetSavedItemForUser(r.Context(), db.GetSavedItemForUserParams{ID: id, UserID: uid})
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if item.Kind != saved.KindArticle || item.Status != saved.StatusDone || !item.ContentPath.Valid {
		writeError(w, http.StatusNotFound, "no content")
		return
	}
	p := filepath.Clean(item.ContentPath.String)
	if !filepath.IsLocal(p) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	f, err := storage.NewLocalDisk(s.storageRoot).Open(p)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, f)
}

// DELETE /me/saved/{id} — remove the record. Files produced in the user's tree
// (downloads, articles) are real nodes with versioning and trash — they are
// deleted through the Files section, not here. Deleting a row mid-download
// also aborts the download goroutine via its next progress UPDATE.
func (s *Server) handleSavedDelete(w http.ResponseWriter, r *http.Request) {
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
	_, err = s.q.DeleteSavedItemForUser(r.Context(), db.DeleteSavedItemForUserParams{ID: id, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
