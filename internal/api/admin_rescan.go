package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/rescan"
)

// POST /admin/rescan {user_id?} — queue a disk reconciliation of one user or of everyone.
func (s *Server) handleAdminRescan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID *string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	var uid pgtype.UUID
	if req.UserID != nil {
		id, err := db.ParseUUID(*req.UserID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid user_id")
			return
		}
		if _, err := s.q.GetUserByID(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		uid = id
	}
	id, err := rescan.Enqueue(r.Context(), s.q, uid, "admin:"+auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// GET /admin/rescan — the latest reconciliation requests and their outcome.
func (s *Server) handleAdminRescanList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListRecentRescanRequests(r.Context(), 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		out = append(out, map[string]any{
			"id": x.ID, "user_email": optText(x.Email), "requested_by": x.RequestedBy,
			"created_at": x.CreatedAt.Time, "started_at": optTime(x.StartedAt),
			"finished_at": optTime(x.FinishedAt), "imported": x.Imported, "missing": x.Missing,
			"changed": x.Changed, "errors": x.Errors, "error_text": optText(x.ErrorText),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func optText(t pgtype.Text) any {
	if !t.Valid {
		return nil
	}
	return t.String
}

func optTime(t pgtype.Timestamptz) any {
	if !t.Valid {
		return nil
	}
	return t.Time
}
