package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// syncSettingsResponse is the shape returned by GET and PUT /me/sync.
type syncSettingsResponse struct {
	Enabled bool           `json:"enabled"`
	Folder  *syncFolderDTO `json:"folder"`
	Epoch   int64          `json:"epoch"`
	// FolderMissing: enabled, but the folder was deleted; the daemon's scoped requests
	// get 409 sync_folder_missing until it is restored or another folder is chosen.
	FolderMissing bool `json:"folder_missing"`
}

type syncFolderDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) buildSyncSettingsResponse(r *http.Request, ss db.SyncSetting, uid pgtype.UUID) syncSettingsResponse {
	resp := syncSettingsResponse{Enabled: ss.Enabled, Epoch: ss.Epoch}
	if ss.FolderNodeID.Valid {
		if node, err := s.q.GetNodeForUser(r.Context(), db.GetNodeForUserParams{ID: ss.FolderNodeID, UserID: uid}); err == nil {
			resp.Folder = &syncFolderDTO{ID: db.UUIDString(node.ID), Name: node.Name}
		}
	}
	resp.FolderMissing = ss.Enabled && resp.Folder == nil
	return resp
}

// GET /me/sync — return the caller's sync-scope settings.
func (s *Server) handleGetSyncSettings(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	ss, err := s.q.GetSyncSettings(r.Context(), uid)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, syncSettingsResponse{Enabled: false, Folder: nil, Epoch: 0})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, s.buildSyncSettingsResponse(r, ss, uid))
}

// PUT /me/sync {"enabled":bool,"folderNodeId":"<uuid>"|null} — upsert sync-scope settings.
func (s *Server) handlePutSyncSettings(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	var req struct {
		Enabled      bool    `json:"enabled"`
		FolderNodeID *string `json:"folderNodeId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	var folderNodeID pgtype.UUID // defaults to invalid (NULL)
	if req.FolderNodeID != nil {
		nid, err := db.ParseUUID(*req.FolderNodeID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid folderNodeId")
			return
		}
		node, err := s.q.GetNodeForUser(r.Context(), db.GetNodeForUserParams{ID: nid, UserID: uid})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "folder not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !node.IsDir {
			writeError(w, http.StatusBadRequest, "folderNodeId must refer to a directory")
			return
		}
		folderNodeID = nid
	}

	// Enabling scope requires a target folder (otherwise "scope" is meaningless).
	if req.Enabled && !folderNodeID.Valid {
		writeError(w, http.StatusBadRequest, "folderNodeId required when enabled")
		return
	}

	ss, err := s.q.UpsertSyncSettings(r.Context(), db.UpsertSyncSettingsParams{
		UserID:       uid,
		Enabled:      req.Enabled,
		FolderNodeID: folderNodeID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, s.buildSyncSettingsResponse(r, ss, uid))
}

// GET /sync/meta — minimal endpoint for the daemon to poll the current scope epoch.
func (s *Server) handleSyncMeta(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	scope, err := s.resolveSyncScope(r.Context(), auth.UserID(r.Context()), uid)
	// The daemon calls this first in every pass and stops the pass on anything but 200,
	// so a missing folder halts it before it pushes or pulls. Other callers only want
	// the epoch.
	if err != nil && (scopeRequested(r) || !errors.Is(err, errSyncFolderMissing)) {
		writeScopeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope_epoch": scope.Epoch})
}

// scopeHeader is the opt-in header the daemon (and the future mobile button-app, which
// runs the same Go sync core) sends so the server applies the user's configured sync
// scope. Browser/web/WebDAV clients do NOT send it and therefore always see the whole
// vault — scoping is daemon-only by design. Not a security boundary: the user owns all
// their files regardless; this only limits what the daemon mirrors locally.
const scopeHeader = "X-Discodrive-Scope"

// scopeRequested reports whether the caller opted into sync-scope (i.e. is the daemon).
func scopeRequested(r *http.Request) bool { return r.Header.Get(scopeHeader) == "1" }

// syncScope is the resolved sync root for a user. RelPrefix is the path under the user's
// root that scopes the feed (e.g. "sync"); "" means whole-vault (scope disabled).
type syncScope struct {
	RelPrefix string
	Epoch     int64
}

// errSyncFolderMissing: the scope is on but its folder is gone — trashed (the setting
// still points at the node), or purged (the foreign key has cleared the setting's
// folder). Falling back to the whole vault would be wrong in both cases: the daemon
// would push its files into the vault root and mirror everything, without a scope
// epoch change to reconcile against. So scoped requests are refused with a stable code
// until the folder is restored from the trash (sync then resumes where it stopped) or
// the user picks another folder or turns the scope off (which bumps the epoch).
var errSyncFolderMissing = errors.New("sync folder is missing")

func (s *Server) resolveSyncScope(ctx context.Context, uidStr string, uid pgtype.UUID) (syncScope, error) {
	ss, err := s.q.GetSyncSettings(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return syncScope{}, nil
	}
	if err != nil {
		return syncScope{}, err
	}
	scope := syncScope{Epoch: ss.Epoch}
	if ss.Enabled {
		if !ss.FolderNodeID.Valid {
			return scope, errSyncFolderMissing
		}
		node, err := s.q.GetNodeForUser(ctx, db.GetNodeForUserParams{ID: ss.FolderNodeID, UserID: uid})
		if errors.Is(err, pgx.ErrNoRows) {
			return scope, errSyncFolderMissing
		}
		if err != nil {
			return syncScope{}, err
		}
		scope.RelPrefix = userRelPath(uidStr, node.DiskPath.String) // strips "<uid>/"
	}
	return scope, nil
}

// writeScopeErr answers a failed scope resolution: 409 with code sync_folder_missing
// when the folder is gone (see errSyncFolderMissing), 500 otherwise.
func writeScopeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errSyncFolderMissing) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "the sync folder is missing: restore it from the trash or choose another folder",
			"code":  "sync_folder_missing",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "internal error")
}

// scopedChangeRows converts the scoped feed's rows to the feed's row shape. dir is the
// scope folder's disk path with a trailing slash. A row whose node now lies outside dir
// matched through prev_path: the node was moved out of the folder. The scoped client
// mirrors only the folder, so for it that move is a delete. It is reported under the
// old path, which stays inside the client's folder (clients delete by node id and
// refuse an entry that resolves to the folder root).
func scopedChangeRows(pr []db.ListChangesSinceUnderPrefixRow, dir string) []db.ListChangesSinceRow {
	rows := make([]db.ListChangesSinceRow, len(pr))
	for i, p := range pr {
		r := db.ListChangesSinceRow{
			Seq: p.Seq, Op: p.Op, Version: p.Version, CreatedAt: p.CreatedAt,
			NodeID: p.NodeID, Name: p.Name, ParentID: p.ParentID, IsDir: p.IsDir,
			Size: p.Size, ContentHash: p.ContentHash, DiskPath: p.DiskPath, Deleted: p.Deleted,
		}
		if !strings.HasPrefix(p.DiskPath.String, dir) && p.PrevPath.Valid {
			r.Op, r.Deleted = "delete", true
			r.DiskPath, r.Name = p.PrevPath, path.Base(p.PrevPath.String)
			r.Size, r.ContentHash = pgtype.Int8{}, pgtype.Text{}
		}
		rows[i] = r
	}
	return rows
}

// escapeLike escapes LIKE wildcards so folder names containing % or _ match literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
