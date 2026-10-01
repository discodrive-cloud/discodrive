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
	"discodrive/internal/quota"
	"discodrive/internal/safecontent"
	"discodrive/internal/storage"
)

// FetchFeedFunc and CoverDownloadFunc are indirection points so tests can bypass
// the SSRF guard (test servers live on 127.0.0.1). Production never reassigns them.
var (
	FetchFeedFunc     = FetchFeed
	CoverDownloadFunc = func(ctx context.Context, src, dst string) (int64, string, string, error) {
		return DownloadToLimit(ctx, src, dst, MaxCoverBytes)
	}
)

// RefreshChannel fetches ch's feed, updates its metadata, upserts new episodes,
// and caches the cover (best-effort). Returns the fetch error if the feed itself
// fails; DB/cover errors are logged, not returned.
func RefreshChannel(ctx context.Context, q *db.Queries, storageRoot string, ch db.PodcastChannel, limits ...*quota.Checker) error {
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
	cacheCover(ctx, q, storageRoot, ch, feed.ImageURL, limits...)
	return nil
}

// cacheCover downloads the channel image once (best-effort) and records its path.
func cacheCover(ctx context.Context, q *db.Queries, storageRoot string, ch db.PodcastChannel, imageURL string, limits ...*quota.Checker) {
	if imageURL == "" || (ch.CoverPath.Valid && ch.CoverPath.String != "") {
		return
	}
	userID := db.UUIDString(ch.UserID)
	chID := db.UUIDString(ch.ID)
	// The extension comes from a URL chosen by the feed's author: keep only
	// raster extensions, and only keep a download whose bytes are a raster image.
	rel := filepath.Join("podcasts", userID, "covers", chID+rasterExt(imageURL))
	if err := storeCover(ctx, q, storageRoot, rel, ch, imageURL, limits...); err != nil {
		log.Printf("discodrive: podcast cover channel=%s: %v", chID, err)
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

// Covers are optional cache data: bound the download before trusting its image header.
const MaxCoverBytes int64 = 2 << 20

func storeCover(ctx context.Context, q *db.Queries, root, rel string, ch db.PodcastChannel, imageURL string, limits ...*quota.Checker) error {
	dir, err := os.MkdirTemp("", "discodrive-cover-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "cover")
	if _, _, _, err = CoverDownloadFunc(ctx, imageURL, dest); err != nil {
		return err
	}
	info, err := os.Stat(dest)
	if err != nil {
		return err
	}
	if info.Size() > MaxCoverBytes {
		return errors.New("podcast: cover too large")
	}
	if _, ok := SniffRasterFile(dest); !ok {
		return errors.New("podcast: cover is not a raster image")
	}
	conn, err := q.AcquireConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	qt := q.WithTx(tx)
	// Serialize publication with quota reservations; charge bytes in the same transaction.
	if err = qt.LockUploadQuota(ctx); err != nil {
		return err
	}
	current, err := qt.LockPodcastChannel(ctx, db.LockPodcastChannelParams{ID: ch.ID, UserID: ch.UserID})
	if err != nil {
		return err
	}
	if current.CoverPath.Valid && current.CoverPath.String != "" {
		return nil
	}
	var total int64
	if len(limits) > 0 {
		total = limits[0].Total()
	}
	if err = quota.New(qt, total).Check(ctx, ch.UserID, info.Size()); err != nil {
		return err
	}
	f, err := os.Open(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	disk := storage.NewLocalDisk(root)
	if _, _, err = disk.WriteFile(rel, f); err != nil {
		_ = disk.Remove(rel)
		return err
	}
	if err = qt.SetPodcastChannelCoverPath(ctx, db.SetPodcastChannelCoverPathParams{ID: ch.ID, UserID: ch.UserID, CoverPath: pgtype.Text{String: rel, Valid: true}, CoverSize: info.Size()}); err != nil {
		_ = disk.Remove(rel)
		return err
	}
	// If commit acknowledgement is lost, keep the bounded file rather than deleting bytes a committed row uses.
	return tx.Commit(ctx)
}
