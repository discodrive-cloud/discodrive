package podcast

import (
	"context"
	"errors"
	"os"

	"discodrive/internal/db"
	"discodrive/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ReconcileCovers runs before workers and HTTP start. Migration 26 cannot stat
// existing cache files: account them here and discard oversized/invalid old cache.
func ReconcileCovers(ctx context.Context, q *db.Queries, root string) error {
	channels, err := q.ListAllPodcastChannels(ctx)
	if err != nil {
		return err
	}
	disk := storage.NewLocalDisk(root)
	for _, ch := range channels {
		if !ch.CoverPath.Valid || ch.CoverPath.String == "" {
			continue
		}
		p := ch.CoverPath
		size := int64(0)
		f, err := disk.Open(p.String)
		switch {
		case errors.Is(err, os.ErrNotExist):
			p = pgtype.Text{}
		case err != nil:
			return err
		default:
			info, e := f.Stat()
			if e != nil {
				f.Close()
				return e
			}
			_, valid := SniffRaster(f)
			f.Close()
			if info.Size() > MaxCoverBytes || !valid {
				if err := disk.Remove(p.String); err != nil {
					return err
				}
				p = pgtype.Text{}
			} else {
				size = info.Size()
			}
		}
		if err := q.SetPodcastChannelCoverPath(ctx, db.SetPodcastChannelCoverPathParams{ID: ch.ID, UserID: ch.UserID, CoverPath: p, CoverSize: size}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteChannel takes the same row lock as cover publication, then reads the
// current paths. A refresh cannot publish an unaccounted cover after deletion.
func DeleteChannel(ctx context.Context, q *db.Queries, root string, id, user pgtype.UUID) (int64, error) {
	conn, err := q.AcquireConnection(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background())
	qt := q.WithTx(tx)
	ch, err := qt.LockPodcastChannel(ctx, db.LockPodcastChannelParams{ID: id, UserID: user})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// A failed removal must not drop the only database reference to the cover.
	if ch.CoverPath.Valid {
		if err := storage.NewLocalDisk(root).Remove(ch.CoverPath.String); err != nil {
			return 0, err
		}
	}
	RemoveChannelFiles(ctx, qt, root, ch)
	n, err := qt.DeletePodcastChannelForUser(ctx, db.DeletePodcastChannelForUserParams{ID: id, UserID: user})
	if err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}
