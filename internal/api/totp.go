package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// GET /me/totp — current 2FA status for the settings UI.
func (s *Server) handleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	row, err := s.q.GetUserTOTP(r.Context(), uid)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"enabled": row.Enabled})
	}
}

// POST /me/totp/setup {approval_token} — start enrolling: returns the otpauth URI + base32 secret (shown once).
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ApprovalToken string `json:"approval_token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request")
		return
	}
	url, secret, err := s.auth.SetupTOTP(r.Context(), auth.UserID(r.Context()), req.ApprovalToken)
	switch {
	case errors.Is(err, auth.ErrApproval):
		writeError(w, http.StatusForbidden, "confirm your identity to enable two-factor auth")
	case errors.Is(err, auth.ErrTOTPNotConfigured):
		writeError(w, http.StatusBadRequest, "two-factor auth is unavailable: server has no encryption key")
	case errors.Is(err, auth.ErrTOTPAlreadyOn):
		writeError(w, http.StatusConflict, "two-factor auth is already enabled")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"otpauth_url": url, "secret": secret})
	}
}

// POST /me/totp/confirm {code, approval_token} — verify the first code, enable 2FA, return one-time backup codes.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var req struct {
		Code          string `json:"code"`
		ApprovalToken string `json:"approval_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	codes, err := s.auth.ConfirmTOTP(r.Context(), auth.UserID(r.Context()), req.Code, req.ApprovalToken)
	switch {
	case errors.Is(err, auth.ErrApproval):
		writeError(w, http.StatusForbidden, "identity confirmation expired or invalid; restart setup")
	case errors.Is(err, auth.ErrTOTPAlreadyOn):
		writeError(w, http.StatusConflict, "two-factor auth is already enabled")
	case errors.Is(err, auth.ErrInvalidTOTPCode):
		writeError(w, http.StatusBadRequest, "invalid code")
	case errors.Is(err, auth.ErrTOTPNotEnabled):
		writeError(w, http.StatusBadRequest, "start setup first")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		uid := auth.UserID(r.Context())
		s.audit(r.Context(), uid, "totp.enabled", r, nil)
		s.notify.Emit(r.Context(), uid, "account.totp_enabled", map[string]any{"Time": time.Now().Format("02.01.2006 15:04")})
		writeJSON(w, http.StatusOK, map[string]any{"backup_codes": codes})
	}
}

// DELETE /me/totp {password, code} — disable 2FA (requires password + a current code).
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	err := s.auth.DisableTOTP(r.Context(), auth.UserID(r.Context()), req.Password, req.Code)
	switch {
	case errors.Is(err, auth.ErrApproval):
		writeError(w, http.StatusUnauthorized, "session expired; sign in again")
	case errors.Is(err, auth.ErrInvalidCreds):
		writeError(w, http.StatusUnauthorized, "invalid password")
	case errors.Is(err, auth.ErrInvalidTOTPCode):
		writeError(w, http.StatusBadRequest, "invalid code")
	case errors.Is(err, auth.ErrTOTPNotEnabled):
		writeError(w, http.StatusBadRequest, "two-factor auth is not enabled")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		uid := auth.UserID(r.Context())
		s.audit(r.Context(), uid, "totp.disabled", r, nil)
		s.notify.Emit(r.Context(), uid, "account.totp_disabled", map[string]any{"Time": time.Now().Format("02.01.2006 15:04")})
		w.WriteHeader(http.StatusNoContent)
	}
}

// POST /auth/mfa/totp {mfa_token, code} — finish a login that needs a second factor.
// Public: it carries the MFA-pending token (the main middleware rejects that token).
// The code may be a TOTP code or a one-time backup code.
func (s *Server) handleMFATOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MFAToken string `json:"mfa_token"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	res, err := s.auth.CompleteMFATOTP(r.Context(), req.MFAToken, req.Code)
	switch {
	case errors.Is(err, auth.ErrInvalidMFAToken):
		writeError(w, http.StatusUnauthorized, "sign-in session expired, start over")
	case errors.Is(err, auth.ErrInvalidTOTPCode):
		writeError(w, http.StatusUnauthorized, "invalid code")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		s.trackLogin(r, res.User)
		event := "login.totp"
		if res.Method == "backup" {
			event = "login.backup_code"
		}
		s.audit(r.Context(), db.UUIDString(res.User.ID), event, r, nil)
		writeJSON(w, http.StatusOK, map[string]any{"token": res.Token, "user": toUserDTO(res.User)})
	}
}
