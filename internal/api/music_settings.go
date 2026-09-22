package api

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// musicSettingsResponse is the shape returned by GET and PUT /me/music.
type musicSettingsResponse struct {
	Enabled           bool            `json:"enabled"`
	Folder            *musicFolderDTO `json:"folder"`
	HasPassword       bool            `json:"hasPassword"`
	TagEditVersioning bool            `json:"tagEditVersioning"`
}

type musicFolderDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// buildMusicSettingsResponse constructs the response from a MusicSetting row.
// If the setting has a valid folder_node_id the node is fetched for its name.
func (s *Server) buildMusicSettingsResponse(r *http.Request, ms db.MusicSetting, uid pgtype.UUID) musicSettingsResponse {
	version, authenticated := auth.TokenVersion(r.Context())
	resp := musicSettingsResponse{
		Enabled:           ms.Enabled,
		HasPassword:       authenticated && ms.TokenVersion == version && ms.PasswordCipher.Valid && ms.PasswordCipher.String != "",
		TagEditVersioning: ms.TagEditVersioning,
	}
	if ms.FolderNodeID.Valid {
		node, err := s.q.GetNodeForUser(r.Context(), db.GetNodeForUserParams{ID: ms.FolderNodeID, UserID: uid})
		if err == nil {
			resp.Folder = &musicFolderDTO{
				ID:   db.UUIDString(node.ID),
				Name: node.Name,
			}
		}
	}
	return resp
}

// GET /me/music — return the caller's music settings.
func (s *Server) handleGetMusicSettings(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	ms, err := s.q.GetMusicSettings(r.Context(), uid)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row yet: return zero-value settings (tagEditVersioning defaults to true).
		writeJSON(w, http.StatusOK, musicSettingsResponse{Enabled: false, Folder: nil, HasPassword: false, TagEditVersioning: true})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, s.buildMusicSettingsResponse(r, ms, uid))
}

// PUT /me/music {"enabled":bool,"folderNodeId":"<uuid>"|null} — upsert music settings.
func (s *Server) handlePutMusicSettings(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	var req struct {
		Enabled           bool    `json:"enabled"`
		FolderNodeID      *string `json:"folderNodeId"`
		TagEditVersioning *bool   `json:"tagEditVersioning"`
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
		// Verify ownership and that the node is a directory.
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

	versioning := true
	if req.TagEditVersioning != nil {
		versioning = *req.TagEditVersioning
	}
	ms, err := s.q.UpsertMusicSettings(r.Context(), db.UpsertMusicSettingsParams{
		UserID:            uid,
		Enabled:           req.Enabled,
		FolderNodeID:      folderNodeID,
		TagEditVersioning: versioning,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, s.buildMusicSettingsResponse(r, ms, uid))
}

// POST /me/music/password — generate a new music password and api_key, store encrypted.
// Returns {"password":"<plaintext>","apiKey":"<key>"} once.
func (s *Server) handlePostMusicPassword(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	if !s.cipher.Enabled() {
		writeError(w, http.StatusInternalServerError, "encryption not configured")
		return
	}

	// Ensure a settings row exists (create one if absent).
	_, err = s.q.GetMusicSettings(r.Context(), uid)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err = s.q.UpsertMusicSettings(r.Context(), db.UpsertMusicSettingsParams{
			UserID:            uid,
			Enabled:           false,
			TagEditVersioning: true,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Generate password: 16 random bytes → base32 lowercase, no padding.
	pwBytes := make([]byte, 16)
	if _, err := rand.Read(pwBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	password := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(pwBytes)
	password = lowercaseBase32(password)

	// Generate api_key: 24 random bytes → hex.
	keyBytes := make([]byte, 24)
	if _, err := rand.Read(keyBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	apiKey := hex.EncodeToString(keyBytes)

	// Encrypt the password before storing.
	ciphertext, err := s.cipher.Encrypt(password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	version, ok := auth.TokenVersion(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authorization required")
		return
	}
	if err := s.q.SetMusicCredentials(r.Context(), db.SetMusicCredentialsParams{
		UserID:         uid,
		PasswordCipher: pgtype.Text{String: ciphertext, Valid: true},
		TokenVersion:   version,
		ApiKey:         pgtype.Text{String: apiKey, Valid: true},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"password": password, "apiKey": apiKey})
}

// DELETE /me/music/password — revoke music password and api_key.
func (s *Server) handleDeleteMusicPassword(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	if err := s.q.ClearMusicCredentials(r.Context(), uid); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// lowercaseBase32 converts a base32 string to lowercase.
func lowercaseBase32(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
