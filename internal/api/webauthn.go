package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

type webauthnCredDTO struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

// GET /me/webauthn — list the user's registered authenticators (no secret material exposed).
func (s *Server) handleWebAuthnList(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	rows, err := s.q.ListWebAuthnCredentials(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]webauthnCredDTO, 0, len(rows))
	for _, c := range rows {
		d := webauthnCredDTO{ID: db.UUIDString(c.ID), Name: c.Name}
		if c.CreatedAt.Valid {
			d.CreatedAt = c.CreatedAt.Time.Format(time.RFC3339)
		}
		if c.LastUsedAt.Valid {
			t := c.LastUsedAt.Time.Format(time.RFC3339)
			d.LastUsedAt = &t
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /me/webauthn/register/begin — challenge for navigator.credentials.create().
func (s *Server) handleWebAuthnRegisterBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ApprovalToken string `json:"approval_token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 403, "identity confirmation required")
		return
	}
	options, sessionToken, err := s.auth.BeginWebAuthnRegistration(r.Context(), auth.UserID(r.Context()), req.ApprovalToken)
	switch {
	case errors.Is(err, auth.ErrApproval):
		writeError(w, 403, "identity confirmation required")
	case errors.Is(err, auth.ErrWebAuthnNotConfigured):
		writeError(w, http.StatusBadRequest, "security keys are unavailable: server has no public domain configured")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"options": json.RawMessage(options), "session_token": sessionToken})
	}
}

// POST /me/webauthn/register/finish {session_token, name, credential} — verify + store.
func (s *Server) handleWebAuthnRegisterFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionToken string          `json:"session_token"`
		Name         string          `json:"name"`
		Credential   json.RawMessage `json:"credential"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	err := s.auth.FinishWebAuthnRegistration(r.Context(), auth.UserID(r.Context()), req.SessionToken, req.Credential, req.Name)
	switch {
	case errors.Is(err, auth.ErrWebAuthnSession):
		writeError(w, http.StatusBadRequest, "registration session expired, start over")
	case errors.Is(err, auth.ErrWebAuthnFailed):
		writeError(w, http.StatusBadRequest, "could not verify the authenticator")
	case errors.Is(err, auth.ErrApproval):
		writeError(w, 403, "identity confirmation required")
	case errors.Is(err, auth.ErrWebAuthnNotConfigured):
		writeError(w, http.StatusBadRequest, "security keys are unavailable")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		uid := auth.UserID(r.Context())
		s.audit(r.Context(), uid, "passkey.added", r, nil)
		s.notify.Emit(r.Context(), uid, "account.passkey_added", map[string]any{"Time": time.Now().Format("02.01.2006 15:04")})
		w.WriteHeader(http.StatusCreated)
	}
}

// POST /auth/webauthn/login/begin — challenge for a passwordless passkey sign-in. Public.
func (s *Server) handleWebAuthnLoginBegin(w http.ResponseWriter, r *http.Request) {
	options, sessionToken, err := s.auth.BeginWebAuthnLogin(r.Context())
	switch {
	case errors.Is(err, auth.ErrApproval):
		writeError(w, 403, "identity confirmation required")
	case errors.Is(err, auth.ErrWebAuthnNotConfigured):
		writeError(w, http.StatusBadRequest, "passkeys are unavailable: server has no public domain configured")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"options": json.RawMessage(options), "session_token": sessionToken})
	}
}

// POST /auth/webauthn/login/finish {session_token, assertion} — verify + issue a session. Public.
func (s *Server) handleWebAuthnLoginFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionToken string          `json:"session_token"`
		Assertion    json.RawMessage `json:"assertion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	res, err := s.auth.FinishWebAuthnLogin(r.Context(), req.SessionToken, req.Assertion)
	switch {
	case errors.Is(err, auth.ErrWebAuthnSession):
		writeError(w, http.StatusUnauthorized, "sign-in session expired, start over")
	case errors.Is(err, auth.ErrWebAuthnFailed):
		writeError(w, http.StatusUnauthorized, "could not verify the passkey")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		s.trackLogin(r, res.User)
		s.audit(r.Context(), db.UUIDString(res.User.ID), "login.passkey", r, nil)
		writeJSON(w, http.StatusOK, map[string]any{"token": res.Token, "user": toUserDTO(res.User)})
	}
}

// PATCH /me/webauthn/{id} {name} — rename an authenticator.
func (s *Server) handleWebAuthnRename(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	cid, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.q.RenameWebAuthnCredential(r.Context(), db.RenameWebAuthnCredentialParams{ID: cid, UserID: uid, Name: req.Name}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /me/webauthn/{id} — remove an authenticator.
func (s *Server) handleWebAuthnDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ApprovalToken string `json:"approval_token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 403, "identity confirmation required")
		return
	}
	if err := s.auth.DeletePasskey(r.Context(), auth.UserID(r.Context()), r.PathValue("id"), req.ApprovalToken); err != nil {
		if errors.Is(err, auth.ErrApproval) {
			writeError(w, 403, "identity confirmation required")
		} else {
			writeError(w, 500, "internal error")
		}
		return
	}
	s.audit(r.Context(), auth.UserID(r.Context()), "passkey.removed", r, nil)
	w.WriteHeader(http.StatusNoContent)
}
