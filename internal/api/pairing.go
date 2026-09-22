package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

const pairingTTL = 10 * time.Minute

// POST /pair/init {name, kind?} — daemon initiates device pairing.
func (s *Server) handlePairInit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = "desktop"
	}
	deviceCode, err := auth.NewDeviceCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	userCode := auth.NewUserCode()
	_, err = s.q.CreatePairing(r.Context(), db.CreatePairingParams{
		DeviceCodeHash: auth.TokenHash(deviceCode),
		UserCode:       userCode,
		ProposedName:   req.Name,
		Kind:           kind,
		ExpiresAt:      pgtype.Timestamptz{Time: time.Now().Add(pairingTTL), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"device_code":      deviceCode,
		"user_code":        userCode,
		"verification_uri": "/app/pair?code=" + userCode,
		"interval":         2,
		"expires_in":       int(pairingTTL.Seconds()),
	})
}

// POST /pair/token {device_code} — daemon polling for pairing result.
func (s *Server) handlePairToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DeviceCode == "" {
		writeError(w, http.StatusBadRequest, "device_code is required")
		return
	}
	p, err := s.q.GetPairingByCodeHash(r.Context(), auth.TokenHash(req.DeviceCode))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.ExpiresAt.Time.Before(time.Now())) {
		writeJSON(w, http.StatusGone, map[string]any{"status": "expired"})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	switch p.Status {
	case "pending":
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
	case "approved":
		// atomically consume the pairing: if two polls race, the token is issued exactly once
		if _, err := s.q.ConsumePairingIfApproved(r.Context(), p.ID); errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusGone, map[string]any{"status": "consumed"})
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		deviceToken, err := auth.NewDeviceToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := s.q.SetDeviceTokenHash(r.Context(), db.SetDeviceTokenHashParams{
			ID: p.DeviceID, TokenHash: pgtype.Text{String: auth.TokenHash(deviceToken), Valid: true},
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "approved", "device_token": deviceToken,
			"device_id": db.UUIDString(p.DeviceID), "name": p.ProposedName,
		})
	default: // consumed
		writeJSON(w, http.StatusGone, map[string]any{"status": "consumed"})
	}
}

// GET /pair/{code} (JWT) — data for the pairing confirmation screen.
func (s *Server) handlePairInfo(w http.ResponseWriter, r *http.Request) {
	p, err := s.q.GetPairingByUserCode(r.Context(), r.PathValue("code"))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (p.ExpiresAt.Time.Before(time.Now()) || p.Status != "pending")) {
		writeError(w, http.StatusNotFound, "pairing code not found or expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"proposed_name": p.ProposedName, "kind": p.Kind, "created_at": p.CreatedAt.Time,
	})
}

// POST /pair/{code}/approve {name} (JWT) — user approves the pairing request.
func (s *Server) handlePairApprove(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	p, err := s.q.GetPairingByUserCode(r.Context(), r.PathValue("code"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "pairing code not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if p.ExpiresAt.Time.Before(time.Now()) {
		writeJSON(w, http.StatusGone, map[string]any{"status": "expired"})
		return
	}
	if p.Status != "pending" {
		writeError(w, http.StatusConflict, "pairing already processed")
		return
	}
	name := req.Name
	if name == "" {
		name = p.ProposedName
	}
	version, ok := auth.TokenVersion(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authorization required")
		return
	}
	dev, err := s.q.CreateDesktopDevice(r.Context(), db.CreateDesktopDeviceParams{UserID: uid, Name: name, TokenVersion: version})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if _, err := s.q.ApprovePairing(r.Context(), db.ApprovePairingParams{
		ID: p.ID, UserID: uid, DeviceID: dev.ID,
	}); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "pairing already processed")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	userID := auth.UserID(r.Context())
	s.audit(r.Context(), userID, "device.paired", r, nil)
	s.notify.Emit(r.Context(), userID, "device.paired", map[string]any{"Name": name, "Time": time.Now().Format("02.01.2006 15:04")})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
