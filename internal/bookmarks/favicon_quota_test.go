package bookmarks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/quota"
)

// Favicons are files on the server's disk, fetched for every bookmark a user syncs: they
// count as the user's data and are not stored past the quota.
func TestFaviconsCountAgainstQuota(t *testing.T) {
	svc, q, uid, root := bootstrap(t)
	ctx := context.Background()
	svc.SetQuota(quota.New(q, 0))
	srv := site(t, true) // icon body "PNGDATA", 7 bytes

	bm, err := q.CreateBrowserBookmark(ctx, db.CreateBrowserBookmarkParams{UserID: uid, Title: "a", Url: srv.URL + "/page"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.EnrichPending(ctx); err != nil {
		t.Fatal(err)
	}
	if used, _ := q.UserStorageUsage(ctx, uid); used != 7 {
		t.Fatalf("usage with one favicon = %d, want 7", used)
	}

	// Out of space: the next favicon is not stored.
	if _, err := q.SetUserQuota(ctx, db.SetUserQuotaParams{ID: uid, StorageQuota: pgtype.Int8{Int64: 10, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	bm2, _ := q.CreateBrowserBookmark(ctx, db.CreateBrowserBookmarkParams{UserID: uid, Title: "b", Url: srv.URL + "/page"})
	if err := svc.EnrichPending(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetBrowserBookmarkForUser(ctx, db.GetBrowserBookmarkForUserParams{ID: bm2.ID, UserID: uid})
	if got.FaviconExt != "" {
		t.Fatalf("favicon stored over quota: ext %q", got.FaviconExt)
	}
	if _, err := os.Stat(filepath.Join(root, faviconRel(db.UUIDString(uid), db.UUIDString(bm2.ID), ".png"))); !os.IsNotExist(err) {
		t.Fatalf("favicon file left on disk over quota: %v", err)
	}
	_ = bm
}
