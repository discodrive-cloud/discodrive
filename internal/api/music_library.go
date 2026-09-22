package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net/http"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/music"
	"discodrive/internal/podcast"
	"discodrive/internal/storage"
)

type radioDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	StreamURL   string `json:"streamUrl"`
	HomepageURL string `json:"homepageUrl"`
}

func (s *Server) handleListRadio(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	rows, err := s.q.ListInternetRadioStations(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]radioDTO, 0, len(rows))
	for _, st := range rows {
		out = append(out, radioDTO{ID: db.UUIDString(st.ID), Name: st.Name, StreamURL: st.StreamUrl, HomepageURL: st.HomepageUrl})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateRadio(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	var body struct {
		Name        string `json:"name"`
		StreamURL   string `json:"streamUrl"`
		HomepageURL string `json:"homepageUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name == "" || body.StreamURL == "" {
		writeError(w, http.StatusBadRequest, "name and streamUrl are required")
		return
	}
	st, err := s.q.CreateInternetRadioStation(r.Context(), db.CreateInternetRadioStationParams{
		UserID: uid, Name: body.Name, StreamUrl: body.StreamURL, HomepageUrl: body.HomepageURL,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, radioDTO{ID: db.UUIDString(st.ID), Name: st.Name, StreamURL: st.StreamUrl, HomepageURL: st.HomepageUrl})
}

func (s *Server) handleUpdateRadio(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var body struct {
		Name        string `json:"name"`
		StreamURL   string `json:"streamUrl"`
		HomepageURL string `json:"homepageUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name == "" || body.StreamURL == "" {
		writeError(w, http.StatusBadRequest, "name and streamUrl are required")
		return
	}
	n, err := s.q.UpdateInternetRadioStation(r.Context(), db.UpdateInternetRadioStationParams{
		ID: id, UserID: uid, Name: body.Name, StreamUrl: body.StreamURL, HomepageUrl: body.HomepageURL,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, radioDTO{ID: db.UUIDString(id), Name: body.Name, StreamURL: body.StreamURL, HomepageURL: body.HomepageURL})
}

func (s *Server) handleDeleteRadio(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	n, err := s.q.DeleteInternetRadioStation(r.Context(), db.DeleteInternetRadioStationParams{ID: id, UserID: uid})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type podcastDTO struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	FeedURL  string `json:"feedUrl"`
	CoverURL string `json:"coverUrl"`
	HasCover bool   `json:"hasCover"`
}

func (s *Server) handleListPodcasts(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	rows, err := s.q.ListPodcastChannelsForUser(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]podcastDTO, 0, len(rows))
	for _, ch := range rows {
		out = append(out, podcastDTO{ID: db.UUIDString(ch.ID), Title: ch.Title, FeedURL: ch.FeedUrl, CoverURL: ch.CoverUrl, HasCover: ch.CoverPath.Valid && ch.CoverPath.String != ""})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreatePodcast(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	existing, err := s.q.GetPodcastChannelByFeed(r.Context(), db.GetPodcastChannelByFeedParams{UserID: uid, FeedUrl: body.URL})
	if err == nil {
		// Already subscribed: refresh best-effort. A temporarily-down feed must
		// not destroy the existing subscription, so RefreshChannel errors are
		// ignored and the channel is never deleted.
		_ = podcast.RefreshChannel(r.Context(), s.q, s.storageRoot, existing)
		fresh, ferr := s.q.GetPodcastChannelForUser(r.Context(), db.GetPodcastChannelForUserParams{ID: existing.ID, UserID: uid})
		if ferr != nil {
			fresh = existing
		}
		writeJSON(w, http.StatusOK, podcastDTO{ID: db.UUIDString(fresh.ID), Title: fresh.Title, FeedURL: fresh.FeedUrl, CoverURL: fresh.CoverUrl, HasCover: fresh.CoverPath.Valid && fresh.CoverPath.String != ""})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// New subscription.
	ch, err := s.q.CreatePodcastChannel(r.Context(), db.CreatePodcastChannelParams{
		UserID: uid, FeedUrl: body.URL,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := podcast.RefreshChannel(r.Context(), s.q, s.storageRoot, ch); err != nil {
		// Bad/unreachable/blocked feed: roll back the just-created channel and report.
		_, _ = s.q.DeletePodcastChannelForUser(r.Context(), db.DeletePodcastChannelForUserParams{ID: ch.ID, UserID: uid})
		writeError(w, http.StatusBadRequest, "could not fetch feed")
		return
	}
	// Re-read to return populated title/cover.
	fresh, err := s.q.GetPodcastChannelForUser(r.Context(), db.GetPodcastChannelForUserParams{ID: ch.ID, UserID: uid})
	if err != nil {
		fresh = ch
	}
	writeJSON(w, http.StatusCreated, podcastDTO{ID: db.UUIDString(fresh.ID), Title: fresh.Title, FeedURL: fresh.FeedUrl, CoverURL: fresh.CoverUrl, HasCover: fresh.CoverPath.Valid && fresh.CoverPath.String != ""})
}

func (s *Server) handleDeletePodcast(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// Best-effort: remove episode files + cover before deleting the row.
	s.removePodcastFiles(r.Context(), id, uid)
	n, err := s.q.DeletePodcastChannelForUser(r.Context(), db.DeletePodcastChannelForUserParams{ID: id, UserID: uid})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetPodcastCover serves the cached channel cover same-origin so the SPA
// can fetch it as a blob (the CSP blocks external img hosts, and auth is a
// Bearer header that a plain <img src> cannot carry).
func (s *Server) handleGetPodcastCover(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	ch, err := s.q.GetPodcastChannelForUser(r.Context(), db.GetPodcastChannelForUserParams{ID: id, UserID: uid})
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !ch.CoverPath.Valid || ch.CoverPath.String == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	path := filepath.Join(s.storageRoot, ch.CoverPath.String)
	f, err := storage.NewLocalDisk(s.storageRoot).Open(ch.CoverPath.String)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	ct := mime.TypeByExtension(filepath.Ext(path))
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// handlePostMusicScan triggers a background library scan for the authenticated user.
// POST /me/music/scan — responds immediately with {"scanning":true}; the scan runs in a goroutine.
func (s *Server) handlePostMusicScan(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	uid, err := db.ParseUUID(userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	ms, err := s.q.GetMusicSettings(r.Context(), uid)
	if err != nil || !ms.FolderNodeID.Valid {
		writeJSON(w, http.StatusOK, map[string]any{"scanning": false})
		return
	}
	folderID := db.UUIDString(ms.FolderNodeID)
	go func() {
		idx := music.NewIndexer(s.q, s.storageRoot)
		if _, err := idx.ScanFolder(context.Background(), userID, folderID); err != nil {
			log.Printf("discodrive: music-scan user=%s: %v", userID, err)
		}
	}()
	writeJSON(w, http.StatusOK, map[string]any{"scanning": true})
}

// removePodcastFiles deletes the cached cover and any downloaded episode files
// for the channel, verifying ownership first. Best-effort: all errors ignored.
func (s *Server) removePodcastFiles(ctx context.Context, channelID, userID pgtype.UUID) {
	ch, err := s.q.GetPodcastChannelForUser(ctx, db.GetPodcastChannelForUserParams{ID: channelID, UserID: userID})
	if err != nil {
		return
	}
	if ch.CoverPath.Valid && ch.CoverPath.String != "" {
		_ = storage.NewLocalDisk(s.storageRoot).Remove(ch.CoverPath.String)
	}
	episodes, err := s.q.ListEpisodesByChannel(ctx, channelID)
	if err != nil {
		return
	}
	for _, ep := range episodes {
		if ep.DiskPath.Valid && ep.DiskPath.String != "" {
			_ = storage.NewLocalDisk(s.storageRoot).Remove(ep.DiskPath.String)
		}
	}
}
