package saved

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// Every saved link used to start its own goroutine, connection and staging file at once:
// a script posting a few thousand slow downloads held all of them open for the two-hour
// download deadline. One user's items now run a few at a time, the rest wait their turn.
func TestOneUsersDownloadsRunAFewAtATime(t *testing.T) {
	var mu sync.Mutex
	active, peak := 0, 0
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	svc, _, q, uid, _ := bootstrap(t, 0)
	ctx := context.Background()
	const total = 12
	ids := make([]pgtype.UUID, 0, total)
	for i := 0; i < total; i++ {
		it, err := svc.Create(ctx, uid, fmt.Sprintf("%s/f%d.bin", srv.URL, i), KindDownload, "", "", "")
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, it.ID)
	}
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	got := peak
	mu.Unlock()
	if got == 0 || got > maxPerUser {
		t.Fatalf("%d downloads of one user ran at once, want 1..%d", got, maxPerUser)
	}

	// Nothing is lost: the waiting ones start as the running ones finish.
	close(release)
	for _, id := range ids {
		waitStatus(t, q, id, uid, StatusDone)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak > maxPerUser {
		t.Fatalf("peak concurrency %d, want at most %d", peak, maxPerUser)
	}
}

// The article HTML sent by the extension (up to 2 MiB) is transport, not storage: it is
// outside the quota, so it must not outlive processing, even when processing fails.
func TestFailedArticleDropsTheClientHTML(t *testing.T) {
	svc, _, q, uid, _ := bootstrap(t, 0)
	ctx := context.Background()
	it, err := svc.Create(ctx, uid, "https://example.test/a", KindArticle, "", strings.Repeat("<div></div>", 1000), "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, q, it.ID, uid, StatusError)
	if got.ContentHtml.Valid {
		t.Errorf("a failed article still holds %d bytes of client HTML", len(got.ContentHtml.String))
	}
}

// Listing must not drag every item's HTML out of the database.
func TestListingDoesNotLoadClientHTML(t *testing.T) {
	_, _, q, uid, _ := bootstrap(t, 0)
	ctx := context.Background()
	if _, err := q.UpsertSavedItem(ctx, db.UpsertSavedItemParams{
		UserID: uid, Url: "https://example.test/a", Kind: KindArticle,
		ContentHtml: pgtype.Text{String: strings.Repeat("x", 1<<20), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := q.ListSavedItems(ctx, db.ListSavedItemsParams{UserID: uid, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %d rows, err %v", len(rows), err)
	}
	if rows[0].ContentHtml.Valid || rows[0].CookieHeader.Valid {
		t.Errorf("the listing carries content_html (%d bytes) or the cookie", len(rows[0].ContentHtml.String))
	}
}

// A user's queue of unfinished items is bounded; past it new links are refused until
// some finish.
func TestQueueOfUnfinishedItemsIsBounded(t *testing.T) {
	svc, _, q, uid, _ := bootstrap(t, 0)
	ctx := context.Background()
	for i := 0; i < maxUnfinishedPerUser; i++ {
		if _, err := q.UpsertSavedItem(ctx, db.UpsertSavedItemParams{
			UserID: uid, Url: fmt.Sprintf("https://example.test/%d", i), Kind: KindDownload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := svc.Create(ctx, uid, "https://example.test/one-more", KindDownload, "", "", "")
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v, want ErrQueueFull", err)
	}
}
