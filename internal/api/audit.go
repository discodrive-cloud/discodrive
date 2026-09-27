package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// audit records a security event for the user, best-effort (a logging failure must never
// break the request that triggered it). ip/user-agent come from the request; detail is
// optional JSON (nil for none).
func (s *Server) audit(ctx context.Context, userID, event string, r *http.Request, detail []byte) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return
	}
	_ = s.q.InsertAuditLog(ctx, db.InsertAuditLogParams{
		UserID:    uid,
		Event:     event,
		Ip:        clientIP(r),
		UserAgent: r.UserAgent(),
		Detail:    detail,
	})
}

type auditDTO struct {
	Event     string `json:"event"`
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
	CreatedAt string `json:"created_at"`
}

// GET /me/audit — the current user's recent security events.
func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	rows, err := s.q.ListAuditLog(r.Context(), db.ListAuditLogParams{UserID: uid, Limit: 50})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]auditDTO, 0, len(rows))
	for _, a := range rows {
		d := auditDTO{Event: a.Event, IP: a.Ip, UserAgent: a.UserAgent}
		if a.CreatedAt.Valid {
			d.CreatedAt = a.CreatedAt.Time.Format(time.RFC3339)
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /me/totp/backup-codes {code} — regenerate one-time backup codes (old ones invalidated).
func (s *Server) handleRegenerateBackupCodes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	uid := auth.UserID(r.Context())
	codes, err := s.auth.RegenerateBackupCodes(r.Context(), uid, req.Code)
	switch {
	case errors.Is(err, auth.ErrApproval):
		writeError(w, http.StatusUnauthorized, "session expired; sign in again")
	case errors.Is(err, auth.ErrInvalidTOTPCode):
		writeError(w, http.StatusBadRequest, "invalid code")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		s.audit(r.Context(), uid, "backup_codes.regenerated", r, nil)
		writeJSON(w, http.StatusOK, map[string]any{"backup_codes": codes})
	}
}
