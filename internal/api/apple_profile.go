package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"discodrive/internal/appleprofile"
	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// POST /me/apple-profile prepares a password-free account profile for Safari.
// The app creates a separately revocable password through /devices/webdav.
func (s *Server) handleAppleProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerURL      string `json:"server_url"`
		InstallationID string `json:"installation_id"`
		Calendars      bool   `json:"calendars"`
		Contacts       bool   `json:"contacts"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.InstallationID) > 128 {
		writeError(w, 400, "invalid profile request")
		return
	}
	// The supplied public origin describes this server, never an arbitrary endpoint
	// to which an installed account could send credentials.
	origin, err := url.Parse(req.ServerURL)
	if err != nil || !strings.EqualFold(origin.Hostname(), (&url.URL{Host: r.Host}).Hostname()) {
		writeError(w, 400, "profile origin must match this server")
		return
	}
	if (req.Calendars && s.getSettingValue(r.Context(), "caldav.enabled") != "true") || (req.Contacts && s.getSettingValue(r.Context(), "carddav.enabled") != "true") {
		writeError(w, 403, "requested DAV service is disabled")
		return
	}
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, 401, "invalid user")
		return
	}
	user, err := s.q.GetUserByID(r.Context(), uid)
	if err != nil {
		writeError(w, 500, "could not load account")
		return
	}
	data, err := appleprofile.Build(appleprofile.Options{ServerURL: req.ServerURL, UserID: db.UUIDString(uid), Email: user.Email, InstallationID: req.InstallationID, Calendars: req.Calendars, Contacts: req.Contacts})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	token, err := s.profiles.Put(db.UUIDString(uid), data)
	if err != nil {
		writeError(w, 429, "too many pending profiles; try again later")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, map[string]string{"download_path": "/apple-profile/" + token + "/DiscoDrive.mobileconfig"})
}

func (s *Server) handleAppleProfileDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	data, ok := s.profiles.Get(r.PathValue("ticket"))
	if !ok {
		http.Error(w, "Profile expired. Please prepare a new profile in DiscoDrive.", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition", `attachment; filename="DiscoDrive.mobileconfig"`)
	_, _ = w.Write(data)
}
