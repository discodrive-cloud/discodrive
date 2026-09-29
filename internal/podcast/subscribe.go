package podcast

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// ErrFeedUnavailable is returned by Subscribe when a new feed cannot be fetched.
var ErrFeedUnavailable = errors.New("podcast: could not fetch feed")

// Subscribe adds feedURL to the user's podcasts and refreshes it.
//
// An existing subscription is refreshed best-effort and survives a failed fetch:
// a feed that is down for a moment must not take the channel, its episodes and
// their downloaded files with it. Only a row this call inserted is rolled back
// when the first fetch fails. created reports which case applied.
func Subscribe(ctx context.Context, q *db.Queries, storageRoot string, userID pgtype.UUID, feedURL string) (ch db.PodcastChannel, created bool, err error) {
	existing, err := q.GetPodcastChannelByFeed(ctx, db.GetPodcastChannelByFeedParams{UserID: userID, FeedUrl: feedURL})
	if err == nil {
		return refreshExisting(ctx, q, storageRoot, existing), false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.PodcastChannel{}, false, err
	}

	ch, err = q.CreatePodcastChannel(ctx, db.CreatePodcastChannelParams{UserID: userID, FeedUrl: feedURL})
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent request subscribed first (ON CONFLICT DO NOTHING): that row
		// is not ours to roll back.
		existing, err := q.GetPodcastChannelByFeed(ctx, db.GetPodcastChannelByFeedParams{UserID: userID, FeedUrl: feedURL})
		if err != nil {
			return db.PodcastChannel{}, false, err
		}
		return refreshExisting(ctx, q, storageRoot, existing), false, nil
	}
	if err != nil {
		return db.PodcastChannel{}, false, err
	}
	if err := RefreshChannel(ctx, q, storageRoot, ch); err != nil {
		RemoveChannelFiles(ctx, q, storageRoot, ch)
		_, _ = q.DeletePodcastChannelForUser(ctx, db.DeletePodcastChannelForUserParams{ID: ch.ID, UserID: userID})
		return db.PodcastChannel{}, false, fmt.Errorf("%w: %v", ErrFeedUnavailable, err)
	}
	return reread(ctx, q, ch), true, nil
}

func refreshExisting(ctx context.Context, q *db.Queries, storageRoot string, ch db.PodcastChannel) db.PodcastChannel {
	_ = RefreshChannel(ctx, q, storageRoot, ch)
	return reread(ctx, q, ch)
}

func reread(ctx context.Context, q *db.Queries, ch db.PodcastChannel) db.PodcastChannel {
	fresh, err := q.GetPodcastChannelForUser(ctx, db.GetPodcastChannelForUserParams{ID: ch.ID, UserID: ch.UserID})
	if err != nil {
		return ch
	}
	return fresh
}

// RemoveChannelFiles deletes the cached cover and every downloaded episode file
// of ch. Call it before deleting the row: once the row is gone the episode paths
// are too, and the files would sit on disk outside any quota. Best-effort.
func RemoveChannelFiles(ctx context.Context, q *db.Queries, storageRoot string, ch db.PodcastChannel) {
	disk := storage.NewLocalDisk(storageRoot)
	if ch.CoverPath.Valid && ch.CoverPath.String != "" {
		_ = disk.Remove(ch.CoverPath.String)
	}
	eps, err := q.ListEpisodesByChannel(ctx, ch.ID)
	if err != nil {
		return
	}
	for _, ep := range eps {
		if ep.DiskPath.Valid && ep.DiskPath.String != "" {
			_ = disk.Remove(ep.DiskPath.String)
		}
	}
}
