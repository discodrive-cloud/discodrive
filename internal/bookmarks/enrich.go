package bookmarks

import (
	"context"
	"log"
	"net/url"
	"time"

	"discodrive/internal/db"
)

// enrichBatch bounds how many bookmarks one worker tick enriches.
const enrichBatch = 20

// perItemDeadline bounds one page+favicon fetch.
const perItemDeadline = 30 * time.Second

// EnrichPending is the worker tick job: it fetches favicons (and titles for
// nodes created without one, e.g. from the web UI) for bookmarks that have not
// been tried yet. Every node is marked as tried regardless of outcome, so a
// dead page is not re-fetched on every tick. Favicon updates do not bump the
// sync cursor; a recovered title does (browsers want it).
func (s *Service) EnrichPending(ctx context.Context) error {
	items, err := s.q.ListBrowserBookmarksNeedingFavicon(ctx, enrichBatch)
	if err != nil {
		return err
	}
	for _, bm := range items {
		ext, size, title := s.enrichOne(ctx, bm)
		if err := s.q.SetBrowserBookmarkFavicon(ctx, db.SetBrowserBookmarkFaviconParams{
			ID:          bm.ID,
			FaviconExt:  ext,
			FaviconSize: size,
		}); err != nil {
			return err
		}
		if bm.Title == "" && title != "" {
			if _, err := s.q.SetBrowserBookmarkTitleIfEmpty(ctx, db.SetBrowserBookmarkTitleIfEmptyParams{
				UserID: bm.UserID,
				ID:     bm.ID,
				Title:  title,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// enrichOne fetches page metadata and the favicon; any failure just yields
// empty results (the node is still marked as tried by the caller).
func (s *Service) enrichOne(ctx context.Context, bm db.BrowserBookmark) (ext string, size int64, title string) {
	if err := s.Validate(bm.Url); err != nil {
		return "", 0, "" // javascript:/about:/private hosts — nothing to fetch
	}
	pageURL, err := url.Parse(bm.Url)
	if err != nil {
		return "", 0, ""
	}
	ctx, cancel := context.WithTimeout(ctx, perItemDeadline)
	defer cancel()
	title, iconHref, err := s.fetchPageMeta(ctx, bm.Url)
	if err != nil {
		log.Printf("discodrive: bookmark enrich %s: %v", bm.Url, err)
		return "", 0, ""
	}
	ext, size = s.fetchFavicon(ctx, bm.UserID, db.UUIDString(bm.ID), pageURL, iconHref)
	return ext, size, title
}
