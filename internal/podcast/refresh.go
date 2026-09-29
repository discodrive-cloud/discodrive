package podcast

import (
	"context"
	"errors"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/safecontent"
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
	// The extension comes from a URL chosen by the feed's author: keep only
	// raster extensions, and only keep a download whose bytes are a raster image.
	rel := filepath.Join("podcasts", userID, "covers", chID+rasterExt(imageURL))
	if _, _, _, err := StoreDownload(storageRoot, rel, func(dest string) (int64, string, string, error) {
		n, ct, suf, err := CoverDownloadFunc(ctx, imageURL, dest)
		if err != nil {
			return n, ct, suf, err
		}
		if _, ok := SniffRasterFile(dest); !ok {
			return 0, "", "", errors.New("podcast: cover is not a raster image")
		}
		return n, ct, suf, nil
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

// rasterExt returns the URL's extension when it names a raster image format,
// ".jpg" otherwise.
func rasterExt(raw string) string {
	switch e := strings.ToLower(extWithDot(raw)); e {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return e
	}
	return ".jpg"
}

// SniffRasterFile identifies an image file by its first bytes (never by its
// name): the allowlisted raster type, or ok=false.
func SniffRasterFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	return SniffRaster(f)
}

// SniffRaster reads up to 512 bytes from r and identifies a raster image.
func SniffRaster(r io.Reader) (string, bool) {
	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", false
	}
	return safecontent.Raster(head[:n])
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
