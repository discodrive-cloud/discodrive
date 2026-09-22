package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/bookmarks"
	"discodrive/internal/db"
	"discodrive/internal/storage"
)

const (
	maxBookmarkTitleLen = 1000
	maxBookmarkURLLen   = 4096
	maxBulkItems        = 20000
	changesPageLimit    = 1000
)

type bookmarkDTO struct {
	ID         string  `json:"id"`
	ParentID   *string `json:"parent_id"`
	IsFolder   bool    `json:"is_folder"`
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Position   int32   `json:"position"`
	HasFavicon bool    `json:"has_favicon"`
	UpdatedAt  string  `json:"updated_at"`
}

type bookmarkChangeDTO struct {
	bookmarkDTO
	Deleted bool  `json:"deleted"`
	Seq     int64 `json:"seq"`
}

func toBookmarkDTO(b db.BrowserBookmark) bookmarkDTO {
	d := bookmarkDTO{
		ID:         db.UUIDString(b.ID),
		IsFolder:   b.IsFolder,
		Title:      b.Title,
		URL:        b.Url,
		Position:   b.Position,
		HasFavicon: b.FaviconExt != "",
		UpdatedAt:  b.UpdatedAt.Time.Format(time.RFC3339),
	}
	if b.ParentID.Valid {
		p := db.UUIDString(b.ParentID)
		d.ParentID = &p
	}
	return d
}

// bookmarkUID resolves the authenticated user or writes a 401.
func (s *Server) bookmarkUID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return pgtype.UUID{}, false
	}
	return uid, true
}

// validBookmarkParent checks that parent is the user's own live folder.
func (s *Server) validBookmarkParent(r *http.Request, uid, parent pgtype.UUID) bool {
	p, err := s.q.GetBrowserBookmarkForUser(r.Context(), db.GetBrowserBookmarkForUserParams{ID: parent, UserID: uid})
	return err == nil && p.IsFolder && !p.Deleted
}

// GET /me/bookmarks — flat list of live nodes (the client builds the tree).
func (s *Server) handleBookmarksList(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.bookmarkUID(w, r)
	if !ok {
		return
	}
	rows, err := s.q.ListBrowserBookmarks(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	dtos := make([]bookmarkDTO, 0, len(rows))
	for _, b := range rows {
		dtos = append(dtos, toBookmarkDTO(b))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// GET /me/bookmarks/changes?since=N — the sync feed for browser extensions:
// every row (tombstones included) with seq > since, plus the safe next cursor.
func (s *Server) handleBookmarksChanges(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.bookmarkUID(w, r)
	if !ok {
		return
	}
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid since")
			return
		}
		since = n
	}
	state, err := s.q.GetBookmarkSyncState(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	type changesResponse struct {
		Cursor     int64               `json:"cursor"`
		FullResync bool                `json:"full_resync"`
		HasMore    bool                `json:"has_more"`
		Items      []bookmarkChangeDTO `json:"items"`
	}
	// A client older than the GC watermark has missed physically deleted
	// tombstones — it must reconcile against the full list instead.
	if since > 0 && since < state.BookmarkGcSeq {
		writeJSON(w, http.StatusOK, changesResponse{Cursor: state.BookmarkSeq, FullResync: true, Items: []bookmarkChangeDTO{}})
		return
	}
	rows, err := s.q.ListBrowserBookmarkChanges(r.Context(), db.ListBrowserBookmarkChangesParams{
		UserID: uid,
		Seq:    since,
		Limit:  changesPageLimit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := changesResponse{Items: make([]bookmarkChangeDTO, 0, len(rows))}
	for _, b := range rows {
		resp.Items = append(resp.Items, bookmarkChangeDTO{bookmarkDTO: toBookmarkDTO(b), Deleted: b.Deleted, Seq: b.Seq})
	}
	if len(rows) == changesPageLimit {
		// Page full: there may be more; the safe cursor is the last row seen.
		resp.HasMore = true
		resp.Cursor = rows[len(rows)-1].Seq
	} else {
		resp.Cursor = state.BookmarkSeq
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /me/bookmarks — create a bookmark or folder. A client-generated id
// makes retries idempotent: re-sending the same id returns the existing row.
func (s *Server) handleBookmarkCreate(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.bookmarkUID(w, r)
	if !ok {
		return
	}
	var req struct {
		ID       string  `json:"id"`
		ParentID *string `json:"parent_id"`
		IsFolder bool    `json:"is_folder"`
		Title    string  `json:"title"`
		URL      string  `json:"url"`
		Position int32   `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(req.Title) > maxBookmarkTitleLen || len(req.URL) > maxBookmarkURLLen {
		writeError(w, http.StatusBadRequest, "title or url too long")
		return
	}
	if req.IsFolder {
		req.URL = ""
	} else if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required for a bookmark")
		return
	}
	var id pgtype.UUID
	if req.ID != "" {
		var err error
		if id, err = db.ParseUUID(req.ID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
	}
	var parent pgtype.UUID
	if req.ParentID != nil && *req.ParentID != "" {
		var err error
		if parent, err = db.ParseUUID(*req.ParentID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid parent_id")
			return
		}
		if !s.validBookmarkParent(r, uid, parent) {
			writeError(w, http.StatusBadRequest, "parent is not your live folder")
			return
		}
	}
	row, err := s.q.CreateBrowserBookmark(r.Context(), db.CreateBrowserBookmarkParams{
		UserID:   uid,
		ID:       id,
		ParentID: parent,
		IsFolder: req.IsFolder,
		Title:    req.Title,
		Url:      req.URL,
		Position: req.Position,
	})
	if err == nil {
		writeJSON(w, http.StatusOK, toBookmarkDTO(row))
		return
	}
	// No row: the id already exists. Idempotent retry → return the existing
	// row; somebody else's id → 409.
	existing, gerr := s.q.GetBrowserBookmarkForUser(r.Context(), db.GetBrowserBookmarkForUserParams{ID: id, UserID: uid})
	if gerr == nil {
		writeJSON(w, http.StatusOK, toBookmarkDTO(existing))
		return
	}
	writeError(w, http.StatusConflict, "id already in use")
}

// POST /me/bookmarks/bulk — initial import of a whole tree in one transaction.
func (s *Server) handleBookmarksBulk(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.bookmarkUID(w, r)
	if !ok {
		return
	}
	var req struct {
		Items []bookmarks.BulkItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(req.Items) == 0 || len(req.Items) > maxBulkItems {
		writeError(w, http.StatusBadRequest, "items must contain 1..20000 entries")
		return
	}
	for i := range req.Items {
		if len(req.Items[i].Title) > maxBookmarkTitleLen || len(req.Items[i].URL) > maxBookmarkURLLen {
			writeError(w, http.StatusBadRequest, "title or url too long")
			return
		}
	}
	count, cursor, err := s.bookmarks.BulkImport(r.Context(), uid, req.Items)
	if err != nil {
		writeError(w, http.StatusBadRequest, "import failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": count, "cursor": cursor})
}

// PATCH /me/bookmarks/{id} — edit title/url/position and/or move to another
// parent (parent_id: "" = the root). A move that would create a cycle is a 409.
func (s *Server) handleBookmarkUpdate(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.bookmarkUID(w, r)
	if !ok {
		return
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		Title    *string `json:"title"`
		URL      *string `json:"url"`
		Position *int32  `json:"position"`
		ParentID *string `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Title != nil && len(*req.Title) > maxBookmarkTitleLen {
		writeError(w, http.StatusBadRequest, "title too long")
		return
	}
	if req.URL != nil && len(*req.URL) > maxBookmarkURLLen {
		writeError(w, http.StatusBadRequest, "url too long")
		return
	}
	current, err := s.q.GetBrowserBookmarkForUser(r.Context(), db.GetBrowserBookmarkForUserParams{ID: id, UserID: uid})
	if err != nil || current.Deleted {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if current.IsFolder && req.URL != nil && *req.URL != "" {
		writeError(w, http.StatusBadRequest, "folders have no url")
		return
	}

	if req.ParentID != nil {
		var parent pgtype.UUID
		if *req.ParentID != "" {
			if parent, err = db.ParseUUID(*req.ParentID); err != nil {
				writeError(w, http.StatusBadRequest, "invalid parent_id")
				return
			}
			if !s.validBookmarkParent(r, uid, parent) {
				writeError(w, http.StatusBadRequest, "parent is not your live folder")
				return
			}
		}
		if _, err := s.q.MoveBrowserBookmark(r.Context(), db.MoveBrowserBookmarkParams{
			UserID:   uid,
			ID:       id,
			ParentID: parent,
		}); err != nil {
			// The anti-cycle guard matched no row.
			writeError(w, http.StatusConflict, "move would create a cycle")
			return
		}
	}

	row := current
	if req.Title != nil || req.URL != nil || req.Position != nil {
		params := db.UpdateBrowserBookmarkParams{UserID: uid, ID: id}
		if req.Title != nil {
			params.Title = pgtype.Text{String: *req.Title, Valid: true}
		}
		if req.URL != nil {
			params.Url = pgtype.Text{String: *req.URL, Valid: true}
		}
		if req.Position != nil {
			params.Position = pgtype.Int4{Int32: *req.Position, Valid: true}
		}
		if row, err = s.q.UpdateBrowserBookmark(r.Context(), params); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	} else if req.ParentID != nil {
		if row, err = s.q.GetBrowserBookmarkForUser(r.Context(), db.GetBrowserBookmarkForUserParams{ID: id, UserID: uid}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	writeJSON(w, http.StatusOK, toBookmarkDTO(row))
}

// DELETE /me/bookmarks/{id} — tombstone the node and its subtree.
func (s *Server) handleBookmarkDelete(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.bookmarkUID(w, r)
	if !ok {
		return
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	n, err := s.bookmarks.Delete(r.Context(), uid, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /me/bookmarks/{id}/favicon — serve the enriched favicon bytes.
func (s *Server) handleBookmarkFavicon(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.bookmarkUID(w, r)
	if !ok {
		return
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	bm, err := s.q.GetBrowserBookmarkForUser(r.Context(), db.GetBrowserBookmarkForUserParams{ID: id, UserID: uid})
	if err != nil || bm.Deleted || bm.FaviconExt == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	rel := filepath.Join("saved", db.UUIDString(uid), "favicons", db.UUIDString(bm.ID)+bm.FaviconExt)
	f, err := storage.NewLocalDisk(s.storageRoot).Open(rel)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	// ServeContent picks the Content-Type from the file extension.
	http.ServeContent(w, r, rel, st.ModTime(), f)
}
