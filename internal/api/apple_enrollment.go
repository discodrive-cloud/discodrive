package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"discodrive/internal/appleprofile"
	"discodrive/internal/auth"
	"discodrive/internal/db"
)

func (s *Server) handleAppleEnrollmentStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]bool{"enabled": s.enrollmentOn})
}

// The credential already lives in the client's Keychain. It is submitted only
// over the authenticated paired-server connection, never in the download URL.
func (s *Server) handleAppleEnrollment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var req struct {
		ServerURL      string `json:"server_url"`
		InstallationID string `json:"installation_id"`
		Calendars      bool   `json:"calendars"`
		Contacts       bool   `json:"contacts"`
		DeviceID       string `json:"device_id"`
		Password       string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.InstallationID) > 128 {
		writeError(w, 400, "invalid enrollment")
		return
	}
	origin, err := url.Parse(req.ServerURL)
	// A different port is a different credential recipient, even on the same host.
	canonical := func(host string) string { return strings.TrimSuffix(strings.ToLower(host), ":443") }
	if err != nil || canonical(origin.Host) != canonical(r.Host) {
		writeError(w, 400, "profile origin must match this server")
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
	opts := appleprofile.Options{ServerURL: strings.TrimRight(req.ServerURL, "/"), UserID: db.UUIDString(uid), Email: user.Email, InstallationID: req.InstallationID, Calendars: req.Calendars, Contacts: req.Contacts}
	if _, err := appleprofile.Build(opts); err != nil || len(req.Password) == 0 || len(req.Password) > 1024 {
		writeError(w, 400, "invalid enrollment")
		return
	}
	if !s.enrollmentServicesEnabled(r, opts) {
		writeError(w, 403, "requested DAV service is disabled")
		return
	}
	did, err := db.ParseUUID(req.DeviceID)
	if err != nil {
		writeError(w, 400, "invalid connection credential")
		return
	}
	dev, err := s.q.GetDevice(r.Context(), did)
	if err != nil || dev.UserID != uid || dev.Kind != "webdav" || !dev.SecretHash.Valid || dev.TokenVersion != user.TokenVersion || user.MustChangePassword {
		writeError(w, 403, "invalid connection credential")
		return
	}
	if ok, _ := auth.VerifyPassword(req.Password, dev.SecretHash.String); !ok {
		writeError(w, 403, "invalid connection credential")
		return
	}
	ticket, err := s.enrollments.Start(opts, req.DeviceID, req.Password)
	if err != nil {
		if errors.Is(err, appleprofile.ErrEnrollmentLimit) {
			writeError(w, 429, "too many pending enrollments; try again later")
		} else {
			writeError(w, 500, "could not prepare enrollment")
		}
		return
	}
	writeJSON(w, 201, map[string]string{"download_path": "/apple-enrollment/" + ticket + "/DiscoDrive.mobileconfig"})
}
func (s *Server) enrollmentServicesEnabled(r *http.Request, o appleprofile.Options) bool {
	return (!o.Calendars || s.getSettingValue(r.Context(), "caldav.enabled") == "true") && (!o.Contacts || s.getSettingValue(r.Context(), "carddav.enabled") == "true")
}
func (s *Server) handleAppleEnrollmentExchange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	id := r.PathValue("ticket")
	opts, deviceID, ok := s.enrollments.Owner(id)
	if !ok {
		http.Error(w, "Enrollment expired. Prepare a new connection in DiscoDrive.", 410)
		return
	}
	did, err := db.ParseUUID(deviceID)
	if err != nil {
		writeError(w, 403, "enrollment revoked")
		return
	}
	dev, err := s.q.GetDevice(r.Context(), did)
	if err != nil || db.UUIDString(dev.UserID) != opts.UserID || dev.Kind != "webdav" {
		s.enrollments.Cancel(id)
		writeError(w, 403, "enrollment revoked")
		return
	}
	user, err := s.q.GetUserByID(r.Context(), dev.UserID)
	if err != nil || user.MustChangePassword || user.TokenVersion != dev.TokenVersion || !s.enrollmentServicesEnabled(r, opts) {
		s.enrollments.Cancel(id)
		writeError(w, 403, "enrollment revoked")
		return
	}
	var data []byte
	contentType := "application/x-apple-aspen-config"
	stage := r.PathValue("stage")
	switch {
	case stage == "DiscoDrive.mobileconfig" && r.Method == "GET":
		data, err = s.enrollments.Initial(id)
		w.Header().Set("Content-Disposition", `attachment; filename="DiscoDrive.mobileconfig"`)
	case stage == "profile" && r.Method == "POST":
		var body []byte
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
		if err == nil {
			data, err = s.enrollments.Profile(id, body)
		}
	case stage == "scep":
		switch r.URL.Query().Get("operation") {
		case "GetCACaps":
			if r.Method != "GET" {
				w.WriteHeader(405)
				return
			}
			contentType = "text/plain"
			data = []byte("POSTPKIOperation\nSHA-256\nAES\n")
		case "GetCACert":
			if r.Method != "GET" {
				w.WriteHeader(405)
				return
			}
			contentType = "application/x-x509-ca-cert"
			data, err = s.enrollments.CACert(id)
		case "PKIOperation":
			contentType = "application/x-pki-message"
			var body []byte
			if r.Method == "POST" {
				body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
			} else if r.Method == "GET" && len(r.URL.RawQuery) <= 180<<10 {
				body, err = base64.StdEncoding.DecodeString(r.URL.Query().Get("message"))
			} else {
				w.WriteHeader(405)
				return
			}
			if err == nil {
				data, err = s.enrollments.Issue(id, body)
			}
		default:
			w.WriteHeader(400)
			return
		}
	default:
		w.WriteHeader(404)
		return
	}
	if err != nil {
		writeError(w, 400, "invalid enrollment exchange")
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}
