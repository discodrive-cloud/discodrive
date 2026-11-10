package subsonic

import (
	"testing"
	"time"

	"discodrive/internal/db"
	"discodrive/internal/podcast"
)

// A download runs in a goroutine and only its "downloading" mark is persisted. A restart
// kills the goroutine and keeps the mark, and a marked episode is never claimed again:
// without recovery at start it could not be downloaded any more.
func TestEpisodeInterruptedByRestart_DownloadsAgain(t *testing.T) {
	restoreFetch := overrideFetchFeed()
	defer restoreFetch()
	restoreDownload := overrideDownloadTo()
	defer restoreDownload()

	h, ctx, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()
	h.xaccel = false

	srv := startRSSServer(t)
	if resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml"); resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}
	epID := getEpisodeID(t, h)
	user, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	_, epUUIDStr, _ := decID(epID)
	epUUID, err := db.ParseUUID(epUUIDStr)
	if err != nil {
		t.Fatalf("ParseUUID: %v", err)
	}

	// What a restart in the middle of a download leaves behind.
	if n, err := h.q.ClaimEpisodeForDownload(ctx, db.ClaimEpisodeForDownloadParams{ID: epUUID, UserID: user.ID}); err != nil || n != 1 {
		t.Fatalf("claim: n=%d err=%v", n, err)
	}

	// The server starts again.
	if err := podcast.RecoverDownloads(ctx, h.q); err != nil {
		t.Fatalf("RecoverDownloads: %v", err)
	}

	if resp := doGet(h, testAPIKey, "downloadPodcastEpisode", "id="+epID); resp == nil || resp["status"] != "ok" {
		t.Fatalf("downloadPodcastEpisode failed: %v", resp)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ep, err := h.q.GetEpisodeForUser(ctx, db.GetEpisodeForUserParams{ID: epUUID, UserID: user.ID})
		if err == nil && ep.Status == "completed" && ep.DiskPath.Valid {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the interrupted episode was not downloaded after the restart")
}
