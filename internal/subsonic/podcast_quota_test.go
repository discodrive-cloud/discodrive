package subsonic

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

// Podcast episodes land in podcasts/<user>/, outside the file tree, and were the one
// write path with no ceiling at all: a subscription with a few hundred episodes could
// fill the disk regardless of the user's quota.
func TestDownloadPodcastEpisode_RefusedWhenQuotaIsFull(t *testing.T) {
	restoreFetch := overrideFetchFeed()
	defer restoreFetch()
	restoreDownload := overrideDownloadTo()
	defer restoreDownload()

	h, ctx, pool := setupWithPool(t)
	h.storageRoot = t.TempDir()
	h.files = storage.NewFileService(pool, storage.NewLocalDisk(h.storageRoot))
	h.files.SetQuota(quota.New(h.q, 0))

	srv := startRSSServer(t)
	if resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml"); resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}
	epID := getEpisodeID(t, h)

	// A quota of one byte, already spent by nothing at all — there is simply no room.
	user, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if _, err := h.q.UpdateUser(ctx, db.UpdateUserParams{
		ID: user.ID, Role: user.Role, StorageQuota: pgtype.Int8{Int64: 1, Valid: true},
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	// Fill it: one 100-byte file leaves the user over quota.
	if _, err := h.q.CreateNode(ctx, db.CreateNodeParams{
		UserID: user.ID, Name: "f.bin", IsDir: false,
		Size: pgtype.Int8{Int64: 100, Valid: true}, DiskPath: pgtype.Text{String: "f.bin", Valid: true},
	}); err != nil {
		t.Fatalf("node: %v", err)
	}

	resp := doGet(h, testAPIKey, "downloadPodcastEpisode", "id="+epID)
	if resp == nil || resp["status"] == "ok" {
		t.Fatalf("download must be refused when the quota is full, got %v", resp)
	}

	// The episode must not have been claimed: a refused download leaves it downloadable
	// again once the user frees space.
	_, epUUIDStr, ok := decID(epID)
	if !ok {
		t.Fatalf("decID(%q) failed", epID)
	}
	epUUID, err := db.ParseUUID(epUUIDStr)
	if err != nil {
		t.Fatalf("ParseUUID: %v", err)
	}
	ep, err := h.q.GetEpisodeForUser(ctx, db.GetEpisodeForUserParams{ID: epUUID, UserID: user.ID})
	if err != nil {
		t.Fatalf("GetEpisodeForUser: %v", err)
	}
	if ep.Status == "downloading" || ep.DiskPath.Valid {
		t.Fatalf("refused episode was claimed anyway: status=%s disk_path=%v", ep.Status, ep.DiskPath)
	}
}

// Parallel downloads of different episodes each read the same free space before they
// start, so the size is charged again at publication, serialized with every other write:
// the second episode that does not fit is refused, not added on top of the first.
func TestPublishEpisode_ChargedAtPublication(t *testing.T) {
	restoreFetch := overrideFetchFeed()
	defer restoreFetch()

	h, ctx, pool := setupWithPool(t)
	h.storageRoot = t.TempDir()
	h.files = storage.NewFileService(pool, storage.NewLocalDisk(h.storageRoot))
	h.files.SetQuota(quota.New(h.q, 0))

	srv := startRSSServer(t)
	if resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml"); resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}
	_, epUUIDStr, _ := decID(getEpisodeID(t, h))
	epUUID, _ := db.ParseUUID(epUUIDStr)
	user, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.UpdateUser(ctx, db.UpdateUserParams{
		ID: user.ID, Role: user.Role, StorageQuota: pgtype.Int8{Int64: 1000, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	// Another write landed while the episode was downloading.
	if _, err := h.q.CreateNode(ctx, db.CreateNodeParams{
		UserID: user.ID, Name: "f.bin", IsDir: false,
		Size: pgtype.Int8{Int64: 600, Valid: true}, DiskPath: pgtype.Text{String: "f.bin", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	arg := db.SetEpisodeDownloadedParams{
		ID: epUUID, UserID: user.ID,
		DiskPath: pgtype.Text{String: "podcasts/x.mp3", Valid: true},
		Size:     pgtype.Int8{Int64: 600, Valid: true}, ContentType: "audio/mpeg", Suffix: "mp3",
	}
	if err := h.publishEpisode(ctx, user.ID, arg); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("publishEpisode over the quota = %v, want ErrExceeded", err)
	}
	ep, _ := h.q.GetEpisodeForUser(ctx, db.GetEpisodeForUserParams{ID: epUUID, UserID: user.ID})
	if ep.DiskPath.Valid {
		t.Fatalf("refused episode was published: %v", ep.DiskPath)
	}
	arg.Size = pgtype.Int8{Int64: 300, Valid: true}
	if err := h.publishEpisode(ctx, user.ID, arg); err != nil {
		t.Fatalf("publishEpisode that fits: %v", err)
	}
}
