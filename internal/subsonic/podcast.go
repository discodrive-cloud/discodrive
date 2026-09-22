package subsonic

import (
	"context"
	"log"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/podcast"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

func init() {
	endpoints["getPodcasts"] = getPodcasts
	endpoints["getNewestPodcasts"] = getNewestPodcasts
	endpoints["createPodcastChannel"] = createPodcastChannel
	endpoints["refreshPodcasts"] = refreshPodcasts
	endpoints["deletePodcastChannel"] = deletePodcastChannel
	endpoints["deletePodcastEpisode"] = deletePodcastEpisode
	endpoints["downloadPodcastEpisode"] = downloadPodcastEpisode
}

// downloadTo is the seam used by tests to override the download function without
// going through the SSRF guard (which blocks 127.0.0.1 httptest servers). The last
// argument is the byte ceiling for one episode (0 = none).
var downloadTo = podcast.DownloadToLimit

// proxyEpisode is the seam used by tests to override on-demand episode proxying
// without going through the SSRF guard (which blocks 127.0.0.1 httptest servers).
var proxyEpisode = podcast.ProxyStream

// suffixFromURL returns the file extension (without leading dot) of the URL path.
// Falls back to "mp3" when no extension is present.
func suffixFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "mp3"
	}
	ext := strings.TrimPrefix(filepath.Ext(u.Path), ".")
	if ext == "" {
		return "mp3"
	}
	return ext
}

// episodeChild builds the Subsonic episode child object for API responses.
func episodeChild(ep db.PodcastEpisode) map[string]any {
	channelUUID := db.UUIDString(ep.ChannelID)
	obj := map[string]any{
		"id":          encID("pe", db.UUIDString(ep.ID)),
		"streamId":    encID("pe", db.UUIDString(ep.ID)),
		"channelId":   encID("pc", channelUUID),
		"title":       ep.Title,
		"description": ep.Description,
		// Always "completed" client-side: every episode is playable, either from
		// disk or via on-demand proxy. The real DB status drives download/retention
		// logic separately. Clients like Amperfy only show a play control for
		// "completed" episodes.
		"status":   "completed",
		"coverArt": encID("pc", channelUUID),
	}
	if ep.Suffix != "" {
		obj["suffix"] = ep.Suffix
	}
	if ep.ContentType != "" {
		obj["contentType"] = ep.ContentType
	}
	if ep.PubDate.Valid {
		obj["publishDate"] = ep.PubDate.Time.UTC().Format("2006-01-02T15:04:05Z")
	}
	if ep.Duration.Valid {
		obj["duration"] = ep.Duration.Int32
	}
	if ep.Size.Valid {
		obj["size"] = ep.Size.Int64
	}
	return obj
}

// refreshOneChannel fetches the feed for one channel and upserts new episodes,
// updates channel metadata, and caches the cover. It delegates to
// podcast.RefreshChannel; userUUID matches ch.UserID for all callers.
func (h *Handler) refreshOneChannel(ctx context.Context, userUUID pgtype.UUID, ch db.PodcastChannel) error {
	return podcast.RefreshChannel(ctx, h.q, h.storageRoot, ch)
}

func getPodcasts(h *Handler, c *reqCtx) {
	ctx := context.Background()

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	includeEpisodes := c.param("includeEpisodes") != "false"
	idParam := c.param("id")

	var channels []db.PodcastChannel
	if idParam != "" {
		kind, uuidStr, ok := decID(idParam)
		if !ok || kind != "pc" {
			c.fail(ErrNotFound, "podcast not found")
			return
		}
		chUUID, err := db.ParseUUID(uuidStr)
		if err != nil {
			c.fail(ErrNotFound, "podcast not found")
			return
		}
		ch, err := h.q.GetPodcastChannelForUser(ctx, db.GetPodcastChannelForUserParams{
			ID:     chUUID,
			UserID: userUUID,
		})
		if err != nil {
			c.fail(ErrNotFound, "podcast not found")
			return
		}
		channels = []db.PodcastChannel{ch}
	} else {
		channels, err = h.q.ListPodcastChannelsForUser(ctx, userUUID)
		if err != nil {
			c.fail(ErrGeneric, "database error")
			return
		}
	}

	result := make([]any, 0, len(channels))
	for _, ch := range channels {
		chUUID := db.UUIDString(ch.ID)
		obj := map[string]any{
			"id":          encID("pc", chUUID),
			"url":         ch.FeedUrl,
			"title":       ch.Title,
			"description": ch.Description,
			"coverArt":    encID("pc", chUUID),
			"status":      "completed",
		}
		if includeEpisodes {
			eps, err := h.q.ListEpisodesByChannel(ctx, ch.ID)
			if err != nil {
				eps = nil
			}
			epList := make([]any, 0, len(eps))
			for _, ep := range eps {
				epList = append(epList, episodeChild(ep))
			}
			obj["episode"] = epList
		}
		result = append(result, obj)
	}

	c.ok(map[string]any{
		"podcasts": map[string]any{
			"channel": result,
		},
	})
}

func getNewestPodcasts(h *Handler, c *reqCtx) {
	ctx := context.Background()

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	count := parseSearchIntParam(c, "count", 20)

	eps, err := h.q.ListNewestEpisodesForUser(ctx, db.ListNewestEpisodesForUserParams{
		UserID: userUUID,
		Limit:  count,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	epList := make([]any, 0, len(eps))
	for _, ep := range eps {
		epList = append(epList, episodeChild(ep))
	}

	c.ok(map[string]any{
		"newestPodcasts": map[string]any{
			"episode": epList,
		},
	})
}

func createPodcastChannel(h *Handler, c *reqCtx) {
	ctx := context.Background()

	feedURL := c.param("url")
	if feedURL == "" {
		c.fail(ErrMissingParam, "Required parameter 'url' is missing")
		return
	}

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	ch, err := h.q.CreatePodcastChannel(ctx, db.CreatePodcastChannelParams{
		UserID:      userUUID,
		FeedUrl:     feedURL,
		Title:       "",
		Description: "",
		CoverUrl:    "",
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	if err := h.refreshOneChannel(ctx, userUUID, ch); err != nil {
		// Fetch failed: clean up the empty channel record.
		_, _ = h.q.DeletePodcastChannelForUser(ctx, db.DeletePodcastChannelForUserParams{
			ID:     ch.ID,
			UserID: userUUID,
		})
		c.fail(ErrGeneric, "could not fetch feed")
		return
	}

	c.ok(map[string]any{})
}

func refreshPodcasts(h *Handler, c *reqCtx) {
	ctx := context.Background()

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	channels, err := h.q.ListPodcastChannelsForUser(ctx, userUUID)
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	for _, ch := range channels {
		if err := h.refreshOneChannel(ctx, userUUID, ch); err != nil {
			log.Printf("discodrive: refreshPodcasts: channel %s: %v", db.UUIDString(ch.ID), err)
		}
	}

	c.ok(map[string]any{})
}

func deletePodcastChannel(h *Handler, c *reqCtx) {
	ctx := context.Background()

	id := c.param("id")
	if id == "" {
		c.fail(ErrMissingParam, "Required parameter 'id' is missing")
		return
	}

	kind, uuidStr, ok := decID(id)
	if !ok || kind != "pc" {
		c.fail(ErrNotFound, "podcast not found")
		return
	}

	chUUID, err := db.ParseUUID(uuidStr)
	if err != nil {
		c.fail(ErrNotFound, "podcast not found")
		return
	}

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	// Verify ownership before touching any files on disk.
	ch, err := h.q.GetPodcastChannelForUser(ctx, db.GetPodcastChannelForUserParams{
		ID:     chUUID,
		UserID: userUUID,
	})
	if err != nil {
		c.fail(ErrNotFound, "channel not found")
		return
	}

	// Best-effort: remove episode files on disk.
	eps, _ := h.q.ListEpisodesByChannel(ctx, chUUID)
	for _, ep := range eps {
		if ep.DiskPath.Valid && ep.DiskPath.String != "" {
			_ = storage.NewLocalDisk(h.storageRoot).Remove(ep.DiskPath.String)
		}
	}

	// Best-effort: remove channel cover file.
	if ch.CoverPath.Valid && ch.CoverPath.String != "" {
		_ = storage.NewLocalDisk(h.storageRoot).Remove(ch.CoverPath.String)
	}

	n, err := h.q.DeletePodcastChannelForUser(ctx, db.DeletePodcastChannelForUserParams{
		ID:     chUUID,
		UserID: userUUID,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}
	if n == 0 {
		c.fail(ErrNotFound, "podcast not found")
		return
	}

	c.ok(map[string]any{})
}

func deletePodcastEpisode(h *Handler, c *reqCtx) {
	ctx := context.Background()

	id := c.param("id")
	if id == "" {
		c.fail(ErrMissingParam, "Required parameter 'id' is missing")
		return
	}

	kind, uuidStr, ok := decID(id)
	if !ok || kind != "pe" {
		c.fail(ErrNotFound, "episode not found")
		return
	}

	epUUID, err := db.ParseUUID(uuidStr)
	if err != nil {
		c.fail(ErrNotFound, "episode not found")
		return
	}

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	ep, err := h.q.GetEpisodeForUser(ctx, db.GetEpisodeForUserParams{
		ID:     epUUID,
		UserID: userUUID,
	})
	if err != nil {
		c.fail(ErrNotFound, "episode not found")
		return
	}

	if ep.DiskPath.Valid && ep.DiskPath.String != "" {
		_ = storage.NewLocalDisk(h.storageRoot).Remove(ep.DiskPath.String)
	}

	n, err := h.q.DeletePodcastEpisodeForUser(ctx, db.DeletePodcastEpisodeForUserParams{
		ID:     epUUID,
		UserID: userUUID,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}
	if n == 0 {
		c.fail(ErrNotFound, "episode not found")
		return
	}

	c.ok(map[string]any{})
}

func downloadPodcastEpisode(h *Handler, c *reqCtx) {
	ctx := context.Background()

	id := c.param("id")
	if id == "" {
		c.fail(ErrMissingParam, "Required parameter 'id' is missing")
		return
	}

	kind, uuidStr, ok := decID(id)
	if !ok || kind != "pe" {
		c.fail(ErrNotFound, "episode not found")
		return
	}

	epUUID, err := db.ParseUUID(uuidStr)
	if err != nil {
		c.fail(ErrNotFound, "episode not found")
		return
	}

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	ep, err := h.q.GetEpisodeForUser(ctx, db.GetEpisodeForUserParams{
		ID:     epUUID,
		UserID: userUUID,
	})
	if err != nil {
		c.fail(ErrNotFound, "episode not found")
		return
	}

	// Episodes are stored outside the file tree (podcasts/<user>/), but they are the
	// user's bytes on the user's disk and count toward the quota like everything else.
	// A feed does not state a trustworthy size, so all we can do up front is refuse
	// when there is no room at all, and hold the stream to what is left.
	allowance, err := h.files.Quota().Allowance(ctx, userUUID)
	if err != nil {
		log.Printf("discodrive: downloadPodcastEpisode quota: %v", err)
		c.fail(ErrGeneric, "database error")
		return
	}
	if allowance <= 0 {
		c.fail(ErrGeneric, "not enough storage left")
		return
	}
	episodeMax := allowance
	if episodeMax == quota.Unlimited {
		episodeMax = 0 // the downloader's "no ceiling"
	}

	// Atomically claim the episode for download. This closes the TOCTOU between
	// reading the status and setting it to "downloading": only one concurrent
	// call can flip a non-downloading/non-completed row, so only one goroutine
	// ever downloads a given episode.
	n, err := h.q.ClaimEpisodeForDownload(ctx, db.ClaimEpisodeForDownloadParams{
		ID:     ep.ID,
		UserID: userUUID,
	})
	if err != nil {
		log.Printf("discodrive: downloadPodcastEpisode claim: %v", err)
		c.fail(ErrGeneric, "database error")
		return
	}
	if n == 0 {
		// Already downloading or completed: idempotent no-op, no goroutine.
		c.ok(map[string]any{})
		return
	}

	// Capture all values by copy before launching the goroutine.
	epID := ep.ID
	audioURL := ep.AudioUrl
	suffix := ep.Suffix
	if suffix == "" {
		suffix = suffixFromURL(audioURL)
	}
	userIDStr := c.userID
	storageRoot := h.storageRoot

	go func() {
		bg := context.Background()
		relPath := filepath.Join("podcasts", userIDStr, db.UUIDString(epID)+"."+suffix)

		// Download into a private temporary directory, then publish safely.
		size, ct, suf, dlErr := podcast.StoreDownload(storageRoot, relPath, func(dest string) (int64, string, string, error) {
			return downloadTo(bg, audioURL, dest, episodeMax)
		})
		if dlErr != nil {
			log.Printf("discodrive: downloadPodcastEpisode download %s: %v", db.UUIDString(epID), dlErr)
			if setErr := h.q.SetEpisodeStatus(bg, db.SetEpisodeStatusParams{
				ID:     epID,
				Status: "error",
				UserID: userUUID,
			}); setErr != nil {
				log.Printf("discodrive: downloadPodcastEpisode set-error-status: %v", setErr)
			}
			return
		}

		if setErr := h.q.SetEpisodeDownloaded(bg, db.SetEpisodeDownloadedParams{
			ID:          epID,
			DiskPath:    pgtype.Text{String: relPath, Valid: true},
			Size:        pgtype.Int8{Int64: size, Valid: true},
			ContentType: ct,
			Suffix:      suf,
			UserID:      userUUID,
		}); setErr != nil {
			log.Printf("discodrive: downloadPodcastEpisode set-downloaded: %v", setErr)
		}
	}()

	c.ok(map[string]any{})
}
