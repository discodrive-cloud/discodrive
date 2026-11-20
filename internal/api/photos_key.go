package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"discodrive/internal/auth"
)

// Device keys for the hidden gallery: a phone registers a P-256 public key once (with a
// photos:hidden approval) and later opens Hidden by signing a server challenge.

// POST /me/photos/v1/hidden/keys {"approvalToken","publicKey" (base64 SPKI DER),"name"} -> {"id"}
func (s *Server) handlePhotosKeyRegister(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	var req struct {
		ApprovalToken string `json:"approvalToken"`
		PublicKey     string `json:"publicKey"`
		Name          string `json:"name"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	spki, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid public key")
		return
	}
	id, err := s.auth.RegisterPhotosKey(r.Context(), auth.UserID(r.Context()), req.ApprovalToken, spki, req.Name)
	if s.photosKeyError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// POST /me/photos/v1/hidden/keys/{id}/challenge -> {"challenge" (base64),"challengeToken"}
func (s *Server) handlePhotosKeyChallenge(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	ch, tok, err := s.auth.PhotosKeyChallenge(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	if s.photosKeyError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"challenge": base64.StdEncoding.EncodeToString(ch), "challengeToken": tok})
}

// POST /me/photos/v1/hidden/keys/{id}/unlock {"challengeToken","challenge","signature"} -> {"token","expiresAt"}
func (s *Server) handlePhotosKeyUnlock(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	var req struct {
		ChallengeToken string `json:"challengeToken"`
		Challenge      string `json:"challenge"`
		Signature      string `json:"signature"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	ch, err1 := base64.StdEncoding.DecodeString(req.Challenge)
	sig, err2 := base64.StdEncoding.DecodeString(req.Signature)
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	tok, exp, err := s.auth.UnlockWithPhotosKey(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), req.ChallengeToken, ch, sig)
	if s.photosKeyError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": tok, "expiresAt": exp.UTC().Format(time.RFC3339)})
}

// DELETE /me/photos/v1/hidden/keys/{id}
func (s *Server) handlePhotosKeyDelete(w http.ResponseWriter, r *http.Request) {
	err := s.auth.DeletePhotosKey(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	if s.photosKeyError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// photosKeyError writes the answer for a key call that failed; false when err is nil.
func (s *Server) photosKeyError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, auth.ErrNoKey):
		writeError(w, http.StatusNotFound, "no such key")
	case errors.Is(err, auth.ErrApproval):
		// 403, not 401: a 401 tells a client its session is over and it signs out.
		writeError(w, http.StatusForbidden, "identity confirmation failed")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
	return true
}
