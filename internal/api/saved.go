package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
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

// isWebURL reports whether raw is an absolute http(s) URL with a host. Bookmarks and
// saved items are rendered as clickable links, so javascript:, data: and friends are
// refused when they are created; rows stored earlier stay readable.
func isWebURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "http" || scheme == "https"
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
	// The URL is shown as a link in the Pocket list and the reader: only web links.
	if !isWebURL(req.URL) {
		writeError(w, http.StatusBadRequest, "url must be an http or https link")
		return
	}
	// With client-supplied content the server never fetches the URL, so the
	// SSRF guard has nothing to protect: addresses behind a paywall or a login
	// are only ever resolved on the client.
	if req.ContentHTML == "" {
		if err := s.saved.Validate(req.URL); err != nil {
			// The guard's error names the addresses the host resolved to (internal DNS
			// included): log it, answer generically.
			log.Printf("discodrive: saved: url refused by the SSRF guard: %v", err)
			writeError(w, http.StatusBadRequest, "url is not allowed")
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

// GET /me/saved/{id}/image?url=… — an image of a saved article, fetched by the server.
// The reader cannot load article images itself: the CSP allows only same-origin, data:
// and blob: images, and loosening it would let any stored article make the browser
// contact third parties. The reader fetches through here (Bearer auth) and shows the
// result as a blob. Only a raster image of at most saved.MaxImageBytes gets through,
// its type sniffed server-side and pinned with nosniff; the upstream never sees the
// user's cookies or a Referer.
func (s *Server) handleSavedImage(w http.ResponseWriter, r *http.Request) {
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
	if err != nil || item.Kind != saved.KindArticle {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	raw := r.URL.Query().Get("url")
	if len(raw) > maxBookmarkURLLen || !isWebURL(raw) {
		writeError(w, http.StatusBadRequest, "url must be an http or https link")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	img, err := s.saved.OpenImage(ctx, raw)
	switch {
	case errors.Is(err, saved.ErrNotImage):
		writeError(w, http.StatusUnsupportedMediaType, "not a supported image")
		return
	case errors.Is(err, saved.ErrImageTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "image too large")
		return
	case err != nil:
		// Blocked by the SSRF guard, unreachable or an upstream error: the details
		// (resolved addresses included) stay in the log.
		log.Printf("discodrive: saved image %s: %v", db.UUIDString(item.ID), err)
		writeError(w, http.StatusBadGateway, "image unavailable")
		return
	}
	defer img.Body.Close()
	h := w.Header()
	h.Set("Content-Type", img.ContentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=86400")
	if img.Size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(img.Size, 10))
	}
	_, _ = io.Copy(w, img.Body)
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
