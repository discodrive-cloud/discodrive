package api

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func validateCreds(email, password string) string {
	if !strings.Contains(email, "@") {
		return "invalid email"
	}
	if len(password) < 8 {
		return "password must be at least 8 characters"
	}
	return ""
}

// --- DTOs (password_hash is never exposed) ---

type userDTO struct {
	ID                 string `json:"id"`
	Email              string `json:"email"`
	Role               string `json:"role"`
	TenantID           string `json:"tenant_id"`
	MustChangePassword bool   `json:"must_change_password"`
}

func toUserDTO(u db.User) userDTO {
	return userDTO{
		ID:                 db.UUIDString(u.ID),
		Email:              u.Email,
		Role:               u.Role,
		TenantID:           db.UUIDString(u.TenantID),
		MustChangePassword: u.MustChangePassword,
	}
}

type nodeDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  *int64 `json:"size"`
	// ModifiedAt is the content's own date when a client supplied one at upload time,
	// otherwise the time we received it.
	ModifiedAt time.Time `json:"modified_at"`
	Version    int64     `json:"version"`
	Mime       string    `json:"mime,omitempty"`
	// ContentHash lets a client ask "is this file already here, byte for byte?" straight
	// from the listing, instead of reconstructing it from the /sync/changes feed.
	ContentHash string `json:"content_hash,omitempty"`
}

func toNodeDTO(n db.Node) nodeDTO {
	d := nodeDTO{ID: db.UUIDString(n.ID), Name: n.Name, IsDir: n.IsDir, Version: n.Version,
		Mime: n.Mime.String, ContentHash: n.ContentHash.String}
	if n.Size.Valid {
		s := n.Size.Int64
		d.Size = &s
	}
	if n.ModifiedAt.Valid {
		d.ModifiedAt = n.ModifiedAt.Time
	}
	return d
}

// clientModTime reads an optional client-supplied content date in RFC3339. An empty value
// means "not supplied" and leaves the server date in place; a malformed one is an error,
// because silently dating a file "now" is worse than telling the client it got it wrong.
func clientModTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, v)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /setup/status — indicates whether initial onboarding is needed (no admin exists yet).
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	needed, err := s.auth.SetupNeeded(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needed": needed, "webauthn": s.auth.WebAuthnEnabled()})
}

// POST /setup/admin — token-less creation of the first admin (only while no admin exists).
func (s *Server) handleSetupAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if msg := validateCreds(req.Email, req.Password); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	admin, err := s.auth.SetupAdmin(r.Context(), req.Email, req.Password)
	switch {
	case errors.Is(err, auth.ErrAdminExists):
		writeError(w, http.StatusConflict, "admin already exists")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		// Fresh install: enable external access (WebDAV/CalDAV/CardDAV) by default.
		s.seedDefaultAccessFlags(r.Context(), admin.ID)
		writeJSON(w, http.StatusCreated, toUserDTO(admin))
	}
}

// POST /auth/register
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if msg := validateCreds(req.Email, req.Password); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	token, user, err := s.auth.Register(r.Context(), req.Email, req.Password)
	switch {
	case errors.Is(err, auth.ErrEmailTaken):
		writeError(w, http.StatusConflict, "email already taken")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"token": token, "user": toUserDTO(user)})
	}
}

// POST /auth/login
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	res, err := s.auth.Login(r.Context(), req.Email, req.Password)
	switch {
	case errors.Is(err, auth.ErrInvalidCreds):
		writeError(w, http.StatusUnauthorized, "invalid email or password")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	case res.MFAToken != "":
		writeJSON(w, http.StatusOK, map[string]any{
			"mfa_required": true,
			"mfa_token":    res.MFAToken,
			"methods":      res.Methods,
		})
	default:
		s.trackLogin(r, res.User)
		s.audit(r.Context(), db.UUIDString(res.User.ID), "login.password", r, nil)
		writeJSON(w, http.StatusOK, map[string]any{"token": res.Token, "user": toUserDTO(res.User)})
	}
}

// PUT /me/password {current_password, new_password} — change the current user's password.
// Invalidates all other sessions; returns a fresh token to the caller.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	token, err := s.auth.ChangePassword(r.Context(), auth.UserID(r.Context()), req.CurrentPassword, req.NewPassword)
	switch {
	case errors.Is(err, auth.ErrInvalidCreds):
		writeError(w, http.StatusUnauthorized, "invalid current password")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		// The middleware already set X-Token with the old token (before the version bump) —
		// overwrite it with the fresh one, otherwise the client will cache a stale token and get 401.
		w.Header().Set("X-Token", token)
		s.audit(r.Context(), auth.UserID(r.Context()), "password.changed", r, nil)
		s.notify.Emit(r.Context(), auth.UserID(r.Context()), "account.password_changed", map[string]any{
			"Time": time.Now().Format("02.01.2006 15:04"),
		})
		writeJSON(w, http.StatusOK, map[string]string{"token": token})
	}
}

// GET /me (authenticated)
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	u, err := s.q.GetUserByID(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(u))
}

// GET /me/storage — the caller's own space: what they occupy, their quota, and how much
// they may still write. A user has to be able to see this without an admin: "not enough
// storage left" on an upload is only actionable if the numbers behind it are visible.
func (s *Server) handleMeStorage(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	user, err := s.q.GetUserByID(r.Context(), uid)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	used, err := s.q.UserStorageUsage(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Trashed files keep costing quota until TRASH_DAYS expires, so "empty the trash"
	// is the fastest way out of a full quota — the number belongs next to the others.
	trash, err := s.q.TrashStorageUsage(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Available is what is left of the user's OWN quota. It deliberately does not take
	// the server-wide cap into account: how full the shared disk is is the operator's
	// business, and reporting it here would show a user without a quota someone else's
	// numbers as if they were their own. A write the cap refuses still fails with 507
	// and a message that says so.
	var userQuota, available *int64
	if user.StorageQuota.Valid {
		q := user.StorageQuota.Int64
		left := max(0, q-used)
		userQuota, available = &q, &left
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"used": used, "quota": userQuota, "available": available, "trash": trash,
	})
}

// GET /files[?parent_id=] (authenticated) — nodes belonging to the current user:
// without parent_id returns root nodes; otherwise returns folder contents (scoped by user_id).
func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}

	var nodes []db.Node
	if pid := r.URL.Query().Get("parent_id"); pid != "" {
		// folder contents (owned or shared) — access check via canAccess
		nodes, err = s.files.ListChildren(r.Context(), auth.UserID(r.Context()), pid)
		if err != nil {
			writeStorageErr(w, err)
			return
		}
	} else {
		nodes, err = s.q.ListRootNodes(r.Context(), uid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	out := make([]nodeDTO, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, toNodeDTO(n))
	}
	writeJSON(w, http.StatusOK, out)
}

func writeStorageErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrInvalidName):
		writeError(w, http.StatusBadRequest, "invalid name")
	case errors.Is(err, storage.ErrNotDir):
		writeError(w, http.StatusBadRequest, "parent is not a folder")
	case errors.Is(err, storage.ErrCycle):
		writeError(w, http.StatusBadRequest, "cannot move a folder into itself")
	case errors.Is(err, storage.ErrNameTaken):
		writeError(w, http.StatusConflict, "name already taken in this folder")
	case errors.Is(err, storage.ErrNotOwner):
		writeError(w, http.StatusForbidden, "only the owner can manage access")
	case errors.Is(err, storage.ErrShareInput):
		writeError(w, http.StatusBadRequest, "invalid access parameters")
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, storage.ErrUploadSize):
		writeError(w, http.StatusBadRequest, "upload is incomplete: staged bytes do not match the declared size")
	case errors.Is(err, quota.ErrExceeded):
		// 507: the request is fine, the storage behind it is not. The message carries the
		// numbers, so clients can tell the user how much room is actually left.
		writeError(w, http.StatusInsufficientStorage, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// POST /files/{id}/share — grant access to a user (email) or create a share link (link).
func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email            *string `json:"email"`
		Link             bool    `json:"link"`
		Access           string  `json:"access"`
		ExpiresInSeconds *int64  `json:"expires_in_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	var exp *time.Time
	if req.ExpiresInSeconds != nil {
		t := time.Now().Add(time.Duration(*req.ExpiresInSeconds) * time.Second)
		exp = &t
	}
	id := r.PathValue("id")
	user := auth.UserID(r.Context())

	switch {
	case req.Link:
		share, token, err := s.files.ShareByLink(r.Context(), user, id, req.Access, exp)
		if err != nil {
			writeStorageErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"share_id": db.UUIDString(share.ID), "token": token, "access": share.Access,
		})
	case req.Email != nil:
		share, err := s.files.ShareToUser(r.Context(), user, id, *req.Email, req.Access, exp)
		if err != nil {
			writeStorageErr(w, err)
			return
		}
		node, nerr := s.files.NodeForDownload(r.Context(), user, id)
		nodeName := ""
		if nerr == nil {
			nodeName = node.Name
		}
		sharerEmail := ""
		if su, e := s.q.GetUserByID(r.Context(), mustUUID(user)); e == nil {
			sharerEmail = su.Email
		}
		s.notify.Emit(r.Context(), db.UUIDString(share.SharedWithUser), "share.received",
			map[string]any{"NodeName": nodeName, "SharerEmail": sharerEmail, "ResourceLabel": "file"})
		writeJSON(w, http.StatusCreated, map[string]any{
			"share_id": db.UUIDString(share.ID), "access": share.Access,
		})
	default:
		writeError(w, http.StatusBadRequest, "email or link is required")
	}
}

// DELETE /shares/{id} — revoke access (owner only)
func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	if err := s.files.Revoke(r.Context(), auth.UserID(r.Context()), r.PathValue("id")); err != nil {
		writeStorageErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /shared — resources shared with the current user (with name and type).
func (s *Server) handleSharedWithMe(w http.ResponseWriter, r *http.Request) {
	shares, err := s.files.SharedWithUser(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(shares))
	for _, sh := range shares {
		node, err := s.q.GetNode(r.Context(), sh.ResourceID)
		if err != nil {
			continue // share pointing to a deleted/non-existent node — skip
		}
		out = append(out, map[string]any{
			"share_id":    db.UUIDString(sh.ID),
			"resource_id": db.UUIDString(sh.ResourceID),
			"name":        node.Name,
			"is_dir":      node.IsDir,
			"access":      sh.Access,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /shared/{id} — the recipient removes the share from their view.
func (s *Server) handleLeaveShare(w http.ResponseWriter, r *http.Request) {
	if err := s.files.LeaveShare(r.Context(), auth.UserID(r.Context()), r.PathValue("id")); err != nil {
		writeStorageErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /s/{token} — public download via share link (no login required).
// Delivery mode is determined by s.xaccel (X-Accel-Redirect or direct stream).
func (s *Server) handleLinkDownload(w http.ResponseWriter, r *http.Request) {
	node, err := s.files.NodeByLink(r.Context(), r.PathValue("token"))
	if err != nil {
		writeStorageErr(w, err) // expired/non-existent link → 404
		return
	}
	if !s.xaccel {
		s.streamFile(w, r, node.Mime, node.Name, node.DiskPath.String)
		return
	}
	setDownloadHeaders(w, node.Mime, node.Name)
	w.Header().Set("X-Accel-Redirect", xaccelRedirect(node.DiskPath.String))
}

// setDownloadHeaders sets safe response headers for user-uploaded files.
// attachment rather than inline: an uploaded HTML/SVG served inline would execute
// as a script on our origin (same as the SPA → JWT theft from localStorage). nosniff
// prevents the browser from sniffing around the declared type. The UI downloads via
// blob URLs, so attachment has no effect on the in-app experience — it only affects
// direct navigation to a public link.
func setDownloadHeaders(w http.ResponseWriter, mime pgtype.Text, name string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if mime.Valid && mime.String != "" {
		w.Header().Set("Content-Type", mime.String)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
}

// POST /files/folder {parent_id?, name}
func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentID *string `json:"parent_id"`
		Name     string  `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	node, err := s.files.CreateFolder(r.Context(), auth.UserID(r.Context()), req.ParentID, req.Name)
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toNodeDTO(node))
}

// maxMultipartUpload caps a single non-resumable upload request body so a client can't
// stream unbounded data and exhaust disk/memory. Larger files use the chunked path.
const maxMultipartUpload = 10 << 30 // 10 GiB

// POST /files/upload (multipart: file, [name], [parent_id])
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMultipartUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "no file in field: file")
		return
	}
	defer file.Close()

	name := r.FormValue("name")
	if name == "" {
		name = hdr.Filename
	}
	var parent *string
	if p := r.FormValue("parent_id"); p != "" {
		parent = &p
	}
	// sync contract: base_version (the version the client was working from) and device.
	var baseVersion *int64
	if bv := r.FormValue("base_version"); bv != "" {
		n, err := strconv.ParseInt(bv, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid base_version")
			return
		}
		baseVersion = &n
	}
	mtime, err := clientModTime(r.FormValue("modified_at"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid modified_at, expected RFC3339")
		return
	}
	res, err := s.files.PushWithMeta(r.Context(), auth.UserID(r.Context()), parent, name, baseVersion,
		r.FormValue("device"), file, storage.PushMeta{ModifiedAt: mtime})
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"node": toNodeDTO(res.Node), "conflicted": res.Conflicted})
}

// PATCH /files/{id}/rename {name}
func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	node, err := s.files.Rename(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), req.Name)
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toNodeDTO(node))
}

// PATCH /files/{id}/move {parent_id?}  (absent/empty parent_id → root)
func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentID *string `json:"parent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	node, err := s.files.Move(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), req.ParentID)
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toNodeDTO(node))
}

// DELETE /files/{id} — soft-delete a node (and its entire subtree)
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.files.Delete(r.Context(), auth.UserID(r.Context()), r.PathValue("id")); err != nil {
		writeStorageErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type versionDTO struct {
	Version         int64  `json:"version"`
	Size            *int64 `json:"size"`
	ContentHash     string `json:"content_hash"`
	IsConflictLoser bool   `json:"is_conflict_loser"`
}

func toVersionDTO(v db.FileVersion) versionDTO {
	d := versionDTO{Version: v.Version, IsConflictLoser: v.IsConflictLoser}
	if v.ContentHash.Valid {
		d.ContentHash = v.ContentHash.String
	}
	if v.Size.Valid {
		s := v.Size.Int64
		d.Size = &s
	}
	return d
}

// GET /files/{id}/versions — version history for a file
func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.files.ListVersions(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	out := make([]versionDTO, 0, len(versions))
	for _, v := range versions {
		out = append(out, toVersionDTO(v))
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /files/{id}/restore {version}
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version int64 `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	node, err := s.files.Restore(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), req.Version)
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toNodeDTO(node))
}

type changeDTO struct {
	Seq         int64  `json:"seq"`
	Op          string `json:"op"`
	Version     int64  `json:"version"`
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	IsDir       bool   `json:"is_dir"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentHash string `json:"content_hash"`
	Deleted     bool   `json:"deleted"`
}

// Delta pagination: default and maximum page size for /sync/changes.
// Clients may reduce it via ?limit= but cannot exceed the ceiling.
const (
	syncDefaultLimit = 500
	syncMaxLimit     = 1000
)

// GET /sync/changes?since=N — delta sync: changes after cursor N
func (s *Server) handleSyncChanges(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		if since, err = strconv.ParseInt(v, 10, 64); err != nil {
			writeError(w, http.StatusBadRequest, "invalid since")
			return
		}
	}
	limit := syncDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		if n > syncMaxLimit {
			n = syncMaxLimit
		}
		limit = n
	}
	uidStr := auth.UserID(r.Context())

	// Resolve the sync scope ONLY for the daemon (it sends X-Discodrive-Scope). Browser/
	// web/WebDAV clients omit the header and always get the whole vault, exactly as before.
	var scope syncScope
	if scopeRequested(r) {
		scope, err = s.resolveSyncScope(r.Context(), uidStr, uid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	// Fetch one extra record beyond the page size: the surplus indicates there are more rows.
	var rows []db.ListChangesSinceRow
	if scope.RelPrefix == "" {
		rows, err = s.q.ListChangesSince(r.Context(), db.ListChangesSinceParams{
			UserID: uid, Seq: since, Lim: int32(limit + 1),
		})
	} else {
		prefix := uidStr + "/" + escapeLike(scope.RelPrefix) + "/%"
		var pr []db.ListChangesSinceUnderPrefixRow
		pr, err = s.q.ListChangesSinceUnderPrefix(r.Context(), db.ListChangesSinceUnderPrefixParams{
			UserID: uid, Seq: since, Lim: int32(limit + 1), Prefix: prefix,
		})
		rows = make([]db.ListChangesSinceRow, len(pr))
		for i, p := range pr {
			rows[i] = db.ListChangesSinceRow(p) // identical column set
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	changes := make([]changeDTO, 0, len(rows))
	cursor := since
	for _, c := range rows {
		rel := userRelPath(uidStr, c.DiskPath.String)
		if scope.RelPrefix != "" {
			rel = strings.TrimPrefix(rel, scope.RelPrefix+"/")
		}
		changes = append(changes, changeDTO{
			Seq: c.Seq, Op: c.Op, Version: c.Version, NodeID: db.UUIDString(c.NodeID),
			Name: c.Name, IsDir: c.IsDir, Path: rel,
			Size: c.Size.Int64, ContentHash: c.ContentHash.String, Deleted: c.Deleted,
		})
		cursor = c.Seq
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"changes": changes, "cursor": cursor, "has_more": hasMore, "scope_epoch": scope.Epoch,
	})
}

// GET /files/{id}/content — download via X-Accel-Redirect (zero-copy) or direct stream
// (XACCEL_ENABLED=false). Access control is always enforced in Go.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	node, err := s.files.NodeForDownload(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		writeStorageErr(w, err) // reject BEFORE X-Accel: access control is not delegated to nginx
		return
	}
	if !s.xaccel {
		s.streamFile(w, r, node.Mime, node.Name, node.DiskPath.String)
		return
	}
	setDownloadHeaders(w, node.Mime, node.Name)
	w.Header().Set("X-Accel-Redirect", xaccelRedirect(node.DiskPath.String))
}

// xaccelRedirect encodes a relative path into an internal nginx URL (spaces/unicode
// in conflict-copy names become %XX; slashes are preserved).
func xaccelRedirect(rel string) string {
	u := url.URL{Path: "/__data/" + rel}
	return u.EscapedPath()
}

// streamFile serves file bytes directly (no-nginx mode): http.ServeContent handles
// Range requests and conditional GETs. Used when XACCEL_ENABLED=false.
func (s *Server) streamFile(w http.ResponseWriter, r *http.Request, mime pgtype.Text, name, diskPath string) {
	s.serveFileContent(w, r, name, diskPath, func() { setDownloadHeaders(w, mime, name) })
}

// serveFileContent opens diskPath and serves it with Range support. setHeaders runs
// only once the file is known to be readable, so error responses stay header-clean.
func (s *Server) serveFileContent(w http.ResponseWriter, r *http.Request, name, diskPath string, setHeaders func()) {
	abs := filepath.Join(s.storageRoot, diskPath)
	f, err := os.Open(abs)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	setHeaders()
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// POST /upload/init {parent_id?, name, size?, modified_at?} → initiates a resumable upload
// session. size is the file's full length: Complete refuses to publish unless the staged
// chunks add up to it. Older clients that omit it keep working, without that check.
//
// A sync client addresses the file by {path, base_version?} instead of parent_id + name:
// path is resolved exactly as PUT /sync/file resolves it (relative to the daemon's scope
// when X-Discodrive-Scope is sent, folder chain created on the way), and base_version is
// the version the client edited — Complete then answers with the same `conflicted` a
// sync PUT would, saving a conflict copy rather than overwriting a newer server version.
func (s *Server) handleUploadInit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentID    *string `json:"parent_id"`
		Name        string  `json:"name"`
		Path        string  `json:"path"`
		BaseVersion *int64  `json:"base_version"`
		Size        int64   `json:"size"`
		ModifiedAt  string  `json:"modified_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Path != "" && (req.Name != "" || req.ParentID != nil) {
		writeError(w, http.StatusBadRequest, "path cannot be combined with name or parent_id")
		return
	}
	if req.Path == "" && req.BaseVersion != nil {
		writeError(w, http.StatusBadRequest, "base_version needs path")
		return
	}
	mtime, err := clientModTime(req.ModifiedAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid modified_at, expected RFC3339")
		return
	}
	var id string
	if req.Path != "" {
		rel, err := s.scopedPushPath(r, req.Path)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		id, err = s.uploads.InitByPath(r.Context(), auth.UserID(r.Context()), rel, req.BaseVersion, req.Size,
			storage.PushMeta{ModifiedAt: mtime})
	} else {
		id, err = s.uploads.Init(r.Context(), auth.UserID(r.Context()), req.ParentID, req.Name, req.Size,
			storage.PushMeta{ModifiedAt: mtime})
	}
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"upload_id": id, "next_chunk": 0})
}

// PUT /upload/{id}/chunk/{n} — body = raw chunk bytes
func (s *Server) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 {
		writeError(w, http.StatusBadRequest, "invalid chunk number")
		return
	}
	next, err := s.uploads.Chunk(r.Context(), r.PathValue("id"), auth.UserID(r.Context()), n, r.Body)
	switch {
	case errors.Is(err, storage.ErrUploadNotFound):
		writeError(w, http.StatusNotFound, "upload session not found")
	case errors.Is(err, storage.ErrChunkOutOfOrder):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "chunk out of order", "next_chunk": next})
	case errors.Is(err, storage.ErrUploadSize):
		writeError(w, http.StatusBadRequest, "chunk exceeds the declared file size")
	case errors.Is(err, quota.ErrExceeded):
		writeError(w, http.StatusInsufficientStorage, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"next_chunk": next})
	}
}

// GET /upload/{id} — status (next expected chunk index) for resuming an upload
func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	next, err := s.uploads.Status(r.PathValue("id"), auth.UserID(r.Context()))
	switch {
	case errors.Is(err, storage.ErrUploadNotFound):
		writeError(w, http.StatusNotFound, "upload session not found")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"next_chunk": next})
	}
}

// DELETE /upload/{id} — cancel the upload and clean up temp files.
func (s *Server) handleUploadAbort(w http.ResponseWriter, r *http.Request) {
	s.uploads.Abort(auth.UserID(r.Context()), r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// POST /upload/{id}/complete — assemble chunks and finalize (via Push)
func (s *Server) handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	res, err := s.uploads.Complete(r.Context(), r.PathValue("id"), auth.UserID(r.Context()))
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"node": toNodeDTO(res.Node), "conflicted": res.Conflicted})
}

// POST /devices/webdav {name} — create an app-specific password for WebDAV.
// The plaintext password is returned exactly once.
func (s *Server) handleCreateWebdavPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "device name is required")
		return
	}
	dev, plain, err := s.auth.CreateWebdavPassword(r.Context(), auth.UserID(r.Context()), req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.notify.Emit(r.Context(), auth.UserID(r.Context()), "device.password_added",
		map[string]any{"DeviceName": dev.Name})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": db.UUIDString(dev.ID), "name": dev.Name, "password": plain,
	})
}

// POST /auth/device/token {device_token} → session JWT.
// Called by the daemon on startup and after a 401. Device revocation → 401.
func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceToken string `json:"device_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DeviceToken == "" {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	tok, err := s.auth.DeviceTokenExchange(r.Context(), req.DeviceToken)
	if errors.Is(err, auth.ErrDeviceToken) {
		writeError(w, http.StatusUnauthorized, "device not authorized")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok})
}

// GET /files/trash — trash bin for the current user.
func (s *Server) handleTrash(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.files.Trash(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		var size *int64
		if n.Size.Valid {
			v := n.Size.Int64
			size = &v
		}
		var deleted any
		if n.DeletedAt.Valid {
			deleted = n.DeletedAt.Time
		}
		out = append(out, map[string]any{
			"id": db.UUIDString(n.ID), "name": n.Name, "is_dir": n.IsDir,
			"size": size, "deleted_at": deleted,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /files/{id}/undelete — restore a file from the trash.
func (s *Server) handleUndelete(w http.ResponseWriter, r *http.Request) {
	node, err := s.files.Undelete(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toNodeDTO(node))
}

// DELETE /files/{id}/purge — permanently delete a file.
func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	if err := s.files.Purge(r.Context(), auth.UserID(r.Context()), r.PathValue("id")); err != nil {
		writeStorageErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /files/trash — empty the entire trash bin.
func (s *Server) handlePurgeAll(w http.ResponseWriter, r *http.Request) {
	if err := s.files.PurgeAll(r.Context(), auth.UserID(r.Context())); err != nil {
		writeStorageErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /files/{id}/shares — outgoing shares for a node (for management purposes).
func (s *Server) handleListNodeShares(w http.ResponseWriter, r *http.Request) {
	shares, err := s.files.SharesForNode(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(shares))
	for _, sh := range shares {
		item := map[string]any{
			"share_id": db.UUIDString(sh.ID), "access": sh.Access,
		}
		if sh.ExpiresAt.Valid {
			item["expires_at"] = sh.ExpiresAt.Time
		}
		if sh.SharedWithUser.Valid {
			item["kind"] = "user"
			if u, err := s.q.GetUserByID(r.Context(), sh.SharedWithUser); err == nil {
				item["email"] = u.Email
			} else {
				item["email"] = ""
			}
		} else {
			item["kind"] = "link"
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /files/{id} (authenticated) — fetch a node by id, scoped by user_id (isolation)
func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	nid, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	node, err := s.q.GetNodeForUser(r.Context(), db.GetNodeForUserParams{ID: nid, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		// another user's node and a missing node are indistinguishable — don't reveal existence
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toNodeDTO(node))
}

func mustUUID(s string) pgtype.UUID { u, _ := db.ParseUUID(s); return u }

// userRelPath strips the "<userID>/" prefix from disk_path, returning a path relative to the user's root.
func userRelPath(userID, diskPath string) string {
	return strings.TrimPrefix(diskPath, userID+"/")
}

func (s *Server) trackLogin(r *http.Request, user db.User) {
	ua := r.UserAgent()
	ip := clientIP(r)
	fp := fmt.Sprintf("%x", sha256.Sum256([]byte(ua)))
	ctx := r.Context()
	prior, _ := s.q.CountUserLogins(ctx, user.ID)
	inserted, err := s.q.UpsertKnownLogin(ctx, db.UpsertKnownLoginParams{
		UserID: user.ID, Fingerprint: fp, UserAgent: ua, Ip: ip,
	})
	if err != nil {
		return
	}
	if inserted && prior > 0 {
		s.notify.Emit(ctx, db.UUIDString(user.ID), "login.new_device",
			map[string]any{"UserAgent": ua, "IP": ip, "Time": time.Now().Format("02.01.2006 15:04")})
	}
}

// clientIP returns the caller's IP for rate limiting. X-Forwarded-For is trusted ONLY
// when the immediate peer is a trusted reverse proxy (our nginx runs on a private or
// loopback network); otherwise a directly-connected client could spoof XFF to bypass
// rate limits. nginx ($proxy_add_x_forwarded_for) appends the real client as the LAST
// entry, so we take the rightmost value.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && isTrustedProxy(host) {
		parts := strings.Split(xff, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	return host
}

// isTrustedProxy reports whether host is a private or loopback address — i.e. our own
// reverse proxy rather than an arbitrary remote client.
func isTrustedProxy(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
