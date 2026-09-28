package api

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/ebook"
)

// ebookSettingsResponse is the shape returned by GET and PUT /me/ebooks.
type ebookSettingsResponse struct {
	Enabled     bool            `json:"enabled"`
	Folder      *ebookFolderDTO `json:"folder"`
	HasPassword bool            `json:"hasPassword"`
	HasApiKey   bool            `json:"hasApiKey"`
}

type ebookFolderDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// buildEbookSettingsResponse constructs the response from an EbookSetting row.
// If the setting has a valid folder_node_id the node is fetched for its name.
func (s *Server) buildEbookSettingsResponse(r *http.Request, es db.EbookSetting, uid pgtype.UUID) ebookSettingsResponse {
	version, authenticated := auth.TokenVersion(r.Context())
	resp := ebookSettingsResponse{
		Enabled:     es.Enabled,
		HasPassword: authenticated && es.TokenVersion == version && es.PasswordCipher.Valid && es.PasswordCipher.String != "",
		HasApiKey:   authenticated && es.TokenVersion == version && es.ApiKey.Valid && es.ApiKey.String != "",
	}
	if es.FolderNodeID.Valid {
		node, err := s.q.GetNodeForUser(r.Context(), db.GetNodeForUserParams{ID: es.FolderNodeID, UserID: uid})
		if err == nil {
			resp.Folder = &ebookFolderDTO{
				ID:   db.UUIDString(node.ID),
				Name: node.Name,
			}
		}
	}
	return resp
}

// GET /me/ebooks — return the caller's ebook settings.
func (s *Server) handleGetEbookSettings(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	es, err := s.q.GetEbookSettings(r.Context(), uid)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row yet: return zero-value settings.
		writeJSON(w, http.StatusOK, ebookSettingsResponse{Enabled: false, Folder: nil, HasPassword: false, HasApiKey: false})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, s.buildEbookSettingsResponse(r, es, uid))
}

// PUT /me/ebooks {"enabled":bool,"folderNodeId":"<uuid>"|null} — upsert ebook settings.
func (s *Server) handlePutEbookSettings(w http.ResponseWriter, r *http.Request) {
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

	prev, err := s.q.GetEbookSettings(r.Context(), uid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	es, err := s.q.UpsertEbookSettings(r.Context(), db.UpsertEbookSettingsParams{
		UserID:       uid,
		Enabled:      req.Enabled,
		FolderNodeID: folderNodeID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if req.Enabled && folderNodeID.Valid && (folderNodeID != prev.FolderNodeID || !prev.Enabled) {
		s.reindexEbookFolder(r.Context(), uid, folderNodeID, folderNodeID != prev.FolderNodeID)
	}
	writeJSON(w, http.StatusOK, s.buildEbookSettingsResponse(r, es, uid))
}

// POST /me/ebooks/password — generate a new ebook password and api_key, store encrypted.
// Returns {"password":"<plaintext>","apiKey":"<key>"} once.
func (s *Server) handlePostEbookPassword(w http.ResponseWriter, r *http.Request) {
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
	_, err = s.q.GetEbookSettings(r.Context(), uid)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err = s.q.UpsertEbookSettings(r.Context(), db.UpsertEbookSettingsParams{
			UserID:  uid,
			Enabled: false,
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
	if err := s.q.SetEbookCredentials(r.Context(), db.SetEbookCredentialsParams{
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

// DELETE /me/ebooks/password — revoke ebook password and api_key.
func (s *Server) handleDeleteEbookPassword(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	if err := s.q.ClearEbookCredentials(r.Context(), uid); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// reindexEbookFolder is reindexMusicFolder for the e-book library.
func (s *Server) reindexEbookFolder(ctx context.Context, uid, folder pgtype.UUID, changed bool) {
	seq, err := s.q.GetUserChangeSeq(ctx, uid)
	if err == nil {
		err = s.q.SetEbookIndexedSeq(ctx, db.SetEbookIndexedSeqParams{UserID: uid, IndexedSeq: seq})
	}
	if err != nil {
		log.Printf("discodrive: ebook cursor after a settings change: %v", err)
	}
	if changed {
		covers, err := s.q.PruneBooksOutsideFolder(ctx, db.PruneBooksOutsideFolderParams{UserID: uid, FolderID: folder})
		if err != nil {
			log.Printf("discodrive: dropping books outside the new ebook folder: %v", err)
		}
		for _, c := range covers {
			if c.Valid && c.String != "" {
				_ = ebook.RemoveCover(s.storageRoot, c.String)
			}
		}
	}
	userID, folderID := db.UUIDString(uid), db.UUIDString(folder)
	go func() {
		if _, err := ebook.NewIndexer(s.q, s.storageRoot).ScanFolder(context.Background(), userID, folderID); err != nil {
			log.Printf("discodrive: ebook scan after a settings change: %v", err)
		}
	}()
}
