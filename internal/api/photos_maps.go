package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"discodrive/internal/photos/maps"
)

// GET /admin/photos/v1/maps -> {region, state, bytes, error, regions}
func (s *Server) handleAdminGetPhotoMaps(w http.ResponseWriter, r *http.Request) {
	if s.maps == nil {
		writeError(w, http.StatusNotFound, "maps are not available")
		return
	}
	st, err := s.maps.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// approxBytes: centralasia is measured, the other sizes are estimates
	type regionDTO struct {
		ID          string `json:"id"`
		ApproxBytes int64  `json:"approxBytes"`
	}
	regions := make([]regionDTO, 0, len(maps.Regions))
	for _, reg := range maps.Regions {
		regions = append(regions, regionDTO{reg.ID, reg.ApproxBytes})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"region": st.Region, "state": st.State, "bytes": st.Bytes, "error": st.Error, "regions": regions,
	})
}

// PUT /admin/photos/v1/maps {"region":"centralasia"|null}: starts the download in the
// background (null removes the maps). Asking again for the region in progress or
// already downloaded changes nothing.
func (s *Server) handleAdminPutPhotoMaps(w http.ResponseWriter, r *http.Request) {
	if s.maps == nil {
		writeError(w, http.StatusNotFound, "maps are not available")
		return
	}
	var req struct {
		Region *string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	region := ""
	if req.Region != nil {
		region = *req.Region
		if region == "" {
			writeError(w, http.StatusBadRequest, "unknown region")
			return
		}
	}
	if err := s.maps.Set(r.Context(), region); errors.Is(err, maps.ErrUnknownRegion) {
		writeError(w, http.StatusBadRequest, "unknown region")
		return
	} else if errors.Is(err, maps.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.handleAdminGetPhotoMaps(w, r)
}

// GET /me/photos/v1/maps/status -> {"ready":bool}
func (s *Server) handlePhotoMapsStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ready": s.maps != nil && s.maps.Ready(r.Context())})
}

// GET /me/photos/v1/maps/region.pmtiles (Range)
func (s *Server) handlePhotoMapsRegion(w http.ResponseWriter, r *http.Request) {
	if s.maps == nil {
		http.NotFound(w, r)
		return
	}
	s.maps.ServeRegion(w, r)
}

// GET /me/photos/v1/maps/assets/{path...}: fonts and sprites of the basemap style.
func (s *Server) handlePhotoMapsAsset(w http.ResponseWriter, r *http.Request) {
	if s.maps == nil {
		http.NotFound(w, r)
		return
	}
	s.maps.ServeAsset(w, r, r.PathValue("path"))
}
