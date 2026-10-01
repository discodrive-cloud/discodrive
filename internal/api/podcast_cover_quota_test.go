package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/podcast"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPodcastCoverLimitsAndAccounting(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{auth: svc, q: q, storageRoot: root, files: storage.NewFileService(pool, storage.NewLocalDisk(root))}
	usePodcastTestFeed(t)
	usePodcastTestCover(t)
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	payload := img.Bytes()
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cover.png" {
			_, _ = w.Write(payload)
			return
		}
		fmt.Fprintf(w, `<rss version="2.0"><channel><title>X</title><image><url>%s/cover.png</url></image></channel></rss>`, upstream.URL)
	}))
	defer upstream.Close()
	tok, u, err := svc.Register(ctx, "cover-limits@test.example", "password12")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name             string
		quota            int64
		large, wantCover bool
	}{
		{"quota", 1, false, false}, {"global", 1 << 30, false, false}, {"oversize", 1 << 30, true, false}, {"valid", 1 << 30, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total := int64(0)
			if tc.name == "global" {
				total = 1
			}
			s.files.SetQuota(quota.New(q, total))
			payload = img.Bytes()
			if tc.large {
				payload = append(append([]byte(nil), payload...), make([]byte, podcast.MaxCoverBytes)...)
			}
			if _, err := q.SetUserQuota(ctx, db.SetUserQuotaParams{ID: u.ID, StorageQuota: pgtype.Int8{Int64: tc.quota, Valid: true}}); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			svc.Middleware(http.HandlerFunc(s.handleCreatePodcast)).ServeHTTP(rec, authedReq("POST", "/me/music/podcasts", tok, fmt.Sprintf(`{"url":%q}`, upstream.URL+"/"+tc.name), ""))
			if rec.Code != 201 {
				t.Fatal(rec.Code, rec.Body)
			}
			var ch podcastDTO
			if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
				t.Fatal(err)
			}
			if ch.HasCover != tc.wantCover {
				t.Fatalf("cover=%v want %v", ch.HasCover, tc.wantCover)
			}
			var diskBytes int64
			if err := filepath.Walk(s.storageRoot, func(_ string, i os.FileInfo, e error) error {
				if e != nil {
					return e
				}
				if !i.IsDir() {
					diskBytes += i.Size()
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			used, err := q.UserStorageUsage(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if used != diskBytes {
				t.Fatalf("disk=%d accounted=%d", diskBytes, used)
			}
			if !tc.wantCover && diskBytes != 0 {
				t.Fatalf("rejected cover left %d bytes", diskBytes)
			}
		})
	}
}

func TestPodcastCoverUpgradeReconcilesOldCache(t *testing.T) {
	ctx := context.Background()
	q, svc, s := buildMusicServer(t)
	s.storageRoot = t.TempDir()
	_, u, err := svc.Register(ctx, "legacy-cover@test.example", "password12")
	if err != nil {
		t.Fatal(err)
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		large bool
	}{{"small", false}, {"large", true}} {
		ch, err := q.CreatePodcastChannel(ctx, db.CreatePodcastChannelParams{UserID: u.ID, FeedUrl: "https://example.org/" + tc.name})
		if err != nil {
			t.Fatal(err)
		}
		rel := filepath.Join("podcasts", db.UUIDString(u.ID), "covers", tc.name+".png")
		content := pngBytes.Bytes()
		if tc.large {
			content = append(append([]byte(nil), content...), make([]byte, podcast.MaxCoverBytes)...)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(s.storageRoot, rel)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.storageRoot, rel), content, 0600); err != nil {
			t.Fatal(err)
		}
		if err := q.SetPodcastChannelCoverPath(ctx, db.SetPodcastChannelCoverPathParams{ID: ch.ID, UserID: u.ID, CoverPath: pgtype.Text{String: rel, Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := podcast.ReconcileCovers(ctx, q, s.storageRoot); err != nil {
		t.Fatal(err)
	}
	used, err := q.UserStorageUsage(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if used != int64(pngBytes.Len()) {
		t.Fatalf("old cache used=%d want=%d", used, pngBytes.Len())
	}
	if _, err := os.Stat(filepath.Join(s.storageRoot, "podcasts", db.UUIDString(u.ID), "covers", "large.png")); !os.IsNotExist(err) {
		t.Fatalf("oversize legacy cover retained: %v", err)
	}
	if err := podcast.ReconcileCovers(ctx, q, s.storageRoot); err != nil {
		t.Fatal(err)
	}
}

func TestPodcastDeleteDuringCoverDownload(t *testing.T) {
	ctx := context.Background()
	q, svc, s := buildMusicServer(t)
	s.storageRoot = t.TempDir()
	_, u, err := svc.Register(ctx, "delete-cover@test.example", "password12")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := q.CreatePodcastChannel(ctx, db.CreatePodcastChannelParams{UserID: u.ID, FeedUrl: "https://example.org/feed"})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	oldFetch, oldDownload := podcast.FetchFeedFunc, podcast.CoverDownloadFunc
	t.Cleanup(func() { podcast.FetchFeedFunc = oldFetch; podcast.CoverDownloadFunc = oldDownload })
	podcast.FetchFeedFunc = func(context.Context, string) (podcast.Feed, error) {
		return podcast.Feed{Title: "x", ImageURL: "https://example.org/cover.png"}, nil
	}
	podcast.CoverDownloadFunc = func(_ context.Context, _ string, dest string) (int64, string, string, error) {
		if err := os.WriteFile(dest, data.Bytes(), 0600); err != nil {
			return 0, "", "", err
		}
		if n, err := podcast.DeleteChannel(ctx, q, s.storageRoot, ch.ID, u.ID); err != nil || n != 1 {
			t.Fatalf("delete=%d,%v", n, err)
		}
		return int64(data.Len()), "image/png", "png", nil
	}
	if err := podcast.RefreshChannel(ctx, q, s.storageRoot, ch); err != nil {
		t.Fatal(err)
	}
	if n, err := podcast.DeleteChannel(ctx, q, s.storageRoot, ch.ID, u.ID); err != nil || n != 0 {
		t.Fatalf("repeat delete=%d,%v", n, err)
	}
	_ = filepath.Walk(s.storageRoot, func(p string, i os.FileInfo, e error) error {
		if e == nil && !i.IsDir() {
			t.Errorf("orphan cover: %s", p)
		}
		return nil
	})
}
