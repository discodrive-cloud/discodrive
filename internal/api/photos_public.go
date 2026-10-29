package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// An album opened by link (/app/a/{token} in the web UI): no account, only the album.
// Every request looks the link up again, so a deleted or expired link closes at once. A
// password, once given, is carried as a key (?k=) a visitor's <img> and <video> can send.

const publicAlbumPage = 500

// albumLink loads the link in the path. open is false while a password stands between the
// visitor and the album.
func (s *Server) albumLink(w http.ResponseWriter, r *http.Request) (link db.GetActivePhotoAlbumLinkRow, open, ok bool) {
	if !s.photosReady(w) {
		return link, false, false
	}
	link, err := s.q.GetActivePhotoAlbumLink(r.Context(), r.PathValue("token"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return link, false, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return link, false, false
	}
	key := r.URL.Query().Get("k")
	if key == "" {
		key = r.Header.Get("X-Album-Key")
	}
	open = !link.PasswordHash.Valid || s.auth.ValidAlbumLink(db.UUIDString(link.ID), key)
	return link, open, true
}

// openAlbumLink is albumLink for what lies behind the password.
func (s *Server) openAlbumLink(w http.ResponseWriter, r *http.Request) (db.GetActivePhotoAlbumLinkRow, bool) {
	link, open, ok := s.albumLink(w, r)
	if !ok {
		return link, false
	}
	if !open {
		writeError(w, http.StatusUnauthorized, "password required")
		return link, false
	}
	return link, true
}

// GET /public/photos/v1/{token}
func (s *Server) handlePublicAlbum(w http.ResponseWriter, r *http.Request) {
	link, open, ok := s.albumLink(w, r)
	if !ok {
		return
	}
	out := map[string]any{"name": link.AlbumName, "locked": !open, "allowDownload": link.AllowDownload}
	if open {
		buckets, err := s.q.ListPhotoAlbumMonthBuckets(r.Context(), link.AlbumID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		var n int64
		for _, b := range buckets {
			n += b.Count
		}
		out["count"] = n
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// POST /public/photos/v1/{token}/unlock {password} → {key}
func (s *Server) handlePublicAlbumUnlock(w http.ResponseWriter, r *http.Request) {
	link, _, ok := s.albumLink(w, r)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if link.PasswordHash.Valid {
		if ok, err := auth.VerifyPassword(req.Password, link.PasswordHash.String); err != nil || !ok {
			writeError(w, http.StatusUnauthorized, "wrong password")
			return
		}
	}
	key := ""
	if link.PasswordHash.Valid {
		var err error
		if key, err = s.auth.IssueAlbumLink(db.UUIDString(link.ID)); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key})
}

// GET /public/photos/v1/{token}/items?after=<takenAt>_<id> — the album newest first.
func (s *Server) handlePublicAlbumItems(w http.ResponseWriter, r *http.Request) {
	link, ok := s.openAlbumLink(w, r)
	if !ok {
		return
	}
	p := db.ListPhotoAlbumAssetsPageParams{AlbumID: link.AlbumID, Lim: publicAlbumPage}
	if v := r.URL.Query().Get("after"); v != "" {
		at, id, found := strings.Cut(v, "_")
		t, err := time.Parse(time.RFC3339Nano, at)
		u, uerr := db.ParseUUID(id)
		if !found || err != nil || uerr != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		p.AfterTaken, p.AfterID = pgtype.Timestamptz{Time: t, Valid: true}, u
	}
	assets, err := s.q.ListPhotoAlbumAssetsPage(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	rows, err := s.photoRows(r, assets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var next *string
	if len(assets) == publicAlbumPage {
		last := assets[len(assets)-1]
		v := last.TakenAt.Time.UTC().Format(time.RFC3339Nano) + "_" + db.UUIDString(last.ID)
		next = &v
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "next": next})
}

// albumAsset is the album's card (or a part of one) named in the path.
func (s *Server) albumAsset(w http.ResponseWriter, r *http.Request) (db.GetActivePhotoAlbumLinkRow, db.PhotoAsset, bool) {
	link, ok := s.openAlbumLink(w, r)
	if !ok {
		return link, db.PhotoAsset{}, false
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return link, db.PhotoAsset{}, false
	}
	a, err := s.q.GetAlbumPhotoAsset(r.Context(), db.GetAlbumPhotoAssetParams{ID: id, AlbumID: link.AlbumID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return link, db.PhotoAsset{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return link, db.PhotoAsset{}, false
	}
	return link, a, true
}

// GET /public/photos/v1/{token}/assets/{id} — what the viewer needs of a card: no path,
// no coordinates, no camera.
func (s *Server) handlePublicAlbumAsset(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.albumAsset(w, r)
	if !ok {
		return
	}
	rows, err := s.photoRows(r, []db.PhotoAsset{a})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	kids, err := s.photoChildren(r, []pgtype.UUID{a.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var liveID *string
	if c, ok := kids[a.ID]["live-video"]; ok {
		v := db.UUIDString(c.ID)
		liveID = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"row": rows[0], "liveVideoId": liveID})
}

// GET /public/photos/v1/{token}/assets/{id}/thumb|preview
func (s *Server) publicAlbumDerivative(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, a, ok := s.albumAsset(w, r)
		if !ok {
			return
		}
		s.serveAssetDerivative(w, r, a, kind)
	}
}

// GET /public/photos/v1/{token}/assets/{id}/original — only where the owner allowed it.
func (s *Server) handlePublicAlbumOriginal(w http.ResponseWriter, r *http.Request) {
	link, a, ok := s.albumAsset(w, r)
	if !ok {
		return
	}
	if !link.AllowDownload {
		writeError(w, http.StatusForbidden, "downloads are off for this link")
		return
	}
	s.serveOriginal(w, r, a, true)
}

// POST /public/photos/v1/{token}/assets/{id}/play {hevc}
func (s *Server) handlePublicAlbumPlay(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.albumAsset(w, r)
	if !ok {
		return
	}
	if a.Kind != "video" {
		writeError(w, http.StatusBadRequest, "not a video")
		return
	}
	var req struct {
		Hevc bool `json:"hevc"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	video := "/public/photos/v1/" + r.PathValue("token") + "/assets/" + db.UUIDString(a.ID) + "/video"
	if k := r.URL.Query().Get("k"); k != "" {
		video += "?k=" + k
	} else {
		video += "?k="
	}
	s.answerPlay(w, r, a, req.Hevc, video+"&src=original", video)
}

// GET /public/photos/v1/{token}/assets/{id}/video
func (s *Server) handlePublicAlbumVideo(w http.ResponseWriter, r *http.Request) {
	link, a, ok := s.albumAsset(w, r)
	if !ok {
		return
	}
	if a.Kind != "video" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// With downloads off, the file itself plays only where it is what the player gets
	// (answerPlay): a codec the browser cannot play comes as the h264 copy, never raw.
	codec := strings.ToLower(a.VideoCodec.String)
	if r.URL.Query().Get("src") == "original" && !link.AllowDownload && codec != "h264" && codec != "hevc" {
		writeError(w, http.StatusForbidden, "downloads are off for this link")
		return
	}
	s.serveVideo(w, r, a)
}
