package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// accessFlagKeys are the global settings that gate external (program) access.
var accessFlagKeys = []string{"webdav.enabled", "caldav.enabled", "carddav.enabled"}

// seedDefaultAccessFlags turns external access on for a fresh install so the
// calendar/files/contacts work out of the box (auth via app-passwords still
// applies). Best-effort: called once when the first admin is created.
func (s *Server) seedDefaultAccessFlags(ctx context.Context, updatedBy pgtype.UUID) {
	for _, key := range accessFlagKeys {
		_ = s.q.UpsertSetting(ctx, db.UpsertSettingParams{Key: key, Value: "true", UpdatedBy: updatedBy})
	}
}

// accessFlags is the on/off state of external (program) access protocols. These are
// global server settings read by the webdav/caldav/carddav middleware: when a flag is
// off the protocol returns 403 and the data is reachable only through the web UI.
type accessFlags struct {
	Webdav  bool `json:"webdav"`
	Caldav  bool `json:"caldav"`
	Carddav bool `json:"carddav"`
}

// GET /me/access — current external-access toggles.
func (s *Server) handleGetAccess(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	writeJSON(w, http.StatusOK, accessFlags{
		Webdav:  s.getSettingValue(ctx, "webdav.enabled") == "true",
		Caldav:  s.getSettingValue(ctx, "caldav.enabled") == "true",
		Carddav: s.getSettingValue(ctx, "carddav.enabled") == "true",
	})
}

// PUT /me/access {"webdav":bool,"caldav":bool,"carddav":bool} — toggle external access.
// Fields are optional; only the keys present in the body are updated.
func (s *Server) handlePutAccess(w http.ResponseWriter, r *http.Request) {
	if auth.Role(r.Context()) != "admin" {
		writeError(w, http.StatusForbidden, "admin privileges required")
		return
	}
	var req struct {
		Webdav  *bool `json:"webdav"`
		Caldav  *bool `json:"caldav"`
		Carddav *bool `json:"carddav"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	updatedBy, _ := db.ParseUUID(auth.UserID(r.Context()))
	set := func(key string, v *bool) error {
		if v == nil {
			return nil
		}
		val := "false"
		if *v {
			val = "true"
		}
		return s.q.UpsertSetting(r.Context(), db.UpsertSettingParams{Key: key, Value: val, UpdatedBy: updatedBy})
	}
	for _, e := range []struct {
		key string
		v   *bool
	}{
		{"webdav.enabled", req.Webdav},
		{"caldav.enabled", req.Caldav},
		{"carddav.enabled", req.Carddav},
	} {
		if err := set(e.key, e.v); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	s.handleGetAccess(w, r)
}
