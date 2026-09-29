package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// scopedPushPath prepends the user's configured sync folder to a client rel-path, but ONLY
// for the daemon (which sends X-Discodrive-Scope). Other clients push to the whole vault.
func (s *Server) scopedPushPath(r *http.Request, rel string) (string, error) {
	if !scopeRequested(r) {
		return rel, nil
	}
	uidStr := auth.UserID(r.Context())
	uid, err := db.ParseUUID(uidStr)
	if err != nil {
		return "", err
	}
	scope, err := s.resolveSyncScope(r.Context(), uidStr, uid)
	if err != nil {
		return "", err
	}
	if scope.RelPrefix == "" {
		return rel, nil
	}
	return scope.RelPrefix + "/" + rel, nil
}

// PUT /sync/file?path=<rel> [X-Base-Version] body=content
func (s *Server) handleSyncPutFile(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	rel, err := s.scopedPushPath(r, rel)
	if err != nil {
		writeScopeErr(w, err)
		return
	}
	var base *int64
	if v := r.Header.Get("X-Base-Version"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid X-Base-Version")
			return
		}
		base = &n
	}
	mtime, err := clientModTime(r.Header.Get("X-Modified-At"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid X-Modified-At, expected RFC3339")
		return
	}
	// Answer before the body: sync clients send the whole file in one PUT with a known
	// length, and a file that cannot fit should cost one round trip, not a full upload
	// that dies at the last byte — every sync cycle, for as long as the quota is full.
	if err := s.files.CheckQuota(r.Context(), auth.UserID(r.Context()), r.ContentLength); err != nil {
		writeStorageErr(w, err)
		return
	}
	res, err := s.files.PushByPathWithMeta(r.Context(), auth.UserID(r.Context()), rel, base, r.Body,
		storage.PushMeta{ModifiedAt: mtime})
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"node": toNodeDTO(res.Node), "conflicted": res.Conflicted})
}

// POST /sync/dir {path}
func (s *Server) handleSyncMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	path, err := s.scopedPushPath(r, req.Path)
	if err != nil {
		writeScopeErr(w, err)
		return
	}
	node, err := s.files.EnsureDirByPath(r.Context(), auth.UserID(r.Context()), path)
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"node": toNodeDTO(node)})
}

// DELETE /sync/file?path=<rel>
func (s *Server) handleSyncDelete(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	rel, err := s.scopedPushPath(r, rel)
	if err != nil {
		writeScopeErr(w, err)
		return
	}
	if err := s.files.DeleteByPath(r.Context(), auth.UserID(r.Context()), rel); err != nil {
		writeStorageErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
