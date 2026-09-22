package podcast

import (
	"context"
	"log"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// FetchFeedFunc and CoverDownloadFunc are indirection points so tests can bypass
// the SSRF guard (test servers live on 127.0.0.1). Production never reassigns them.
var (
	FetchFeedFunc     = FetchFeed
	CoverDownloadFunc = DownloadTo
)

// RefreshChannel fetches ch's feed, updates its metadata, upserts new episodes,
// and caches the cover (best-effort). Returns the fetch error if the feed itself
// fails; DB/cover errors are logged, not returned.
func RefreshChannel(ctx context.Context, q *db.Queries, storageRoot string, ch db.PodcastChannel) error {
	feed, err := FetchFeedFunc(ctx, ch.FeedUrl)
	if err != nil {
		return err
	}
	if err := q.SetPodcastChannelMeta(ctx, db.SetPodcastChannelMetaParams{
		ID: ch.ID, UserID: ch.UserID, Title: feed.Title, Description: feed.Description, CoverUrl: feed.ImageURL,
	}); err != nil {
		log.Printf("discodrive: podcast set-meta channel=%s: %v", db.UUIDString(ch.ID), err)
	}
	for _, ep := range feed.Episodes {
		var pub pgtype.Timestamptz
		if ep.PubDate != nil {
			pub = pgtype.Timestamptz{Time: *ep.PubDate, Valid: true}
		}
		dur := pgtype.Int4{}
		if ep.Duration > 0 {
			dur = pgtype.Int4{Int32: int32(ep.Duration), Valid: true}
		}
		if err := q.UpsertPodcastEpisode(ctx, db.UpsertPodcastEpisodeParams{
			ChannelID: ch.ID, UserID: ch.UserID, Title: ep.Title, Description: ep.Description,
			PubDate: pub, AudioUrl: ep.AudioURL, Duration: dur,
			Suffix: extNoDot(ep.AudioURL), ContentType: ep.ContentType,
		}); err != nil {
			log.Printf("discodrive: podcast upsert-episode channel=%s: %v", db.UUIDString(ch.ID), err)
		}
	}
	cacheCover(ctx, q, storageRoot, ch, feed.ImageURL)
	return nil
}

// cacheCover downloads the channel image once (best-effort) and records its path.
func cacheCover(ctx context.Context, q *db.Queries, storageRoot string, ch db.PodcastChannel, imageURL string) {
	if imageURL == "" || (ch.CoverPath.Valid && ch.CoverPath.String != "") {
		return
	}
	userID := db.UUIDString(ch.UserID)
	chID := db.UUIDString(ch.ID)
	rel := filepath.Join("podcasts", userID, "covers", chID+extWithDot(imageURL))
	if _, _, _, err := StoreDownload(storageRoot, rel, func(dest string) (int64, string, string, error) {
		return CoverDownloadFunc(ctx, imageURL, dest)
	}); err != nil {
		log.Printf("discodrive: podcast cover channel=%s: %v", chID, err)
		return
	}
	if err := q.SetPodcastChannelCoverPath(ctx, db.SetPodcastChannelCoverPathParams{
		ID: ch.ID, CoverPath: pgtype.Text{String: rel, Valid: true}, UserID: ch.UserID,
	}); err != nil {
		log.Printf("discodrive: podcast cover-path channel=%s: %v", chID, err)
	}
}

// extNoDot returns the URL path extension without the dot (default "mp3").
func extNoDot(raw string) string {
	e := extWithDot(raw)
	if e == "" {
		return "mp3"
	}
	return strings.ToLower(strings.TrimPrefix(e, "."))
}

// extWithDot returns the URL path extension including the dot (default ".jpg" for empty).
func extWithDot(raw string) string {
	u, err := url.Parse(raw)
	p := raw
	if err == nil {
		p = u.Path
	}
	e := filepath.Ext(p)
	if e == "" {
		return ".jpg"
	}
	return e
}
