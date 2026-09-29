package saved

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"discodrive/internal/db"
)

type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) { panic("parser blew up") }

// A panic while processing an item (the parsers get up to 2 MiB of user HTML) killed
// the server, and RecoverStale re-queued the item on restart: a crash loop. The item
// must end up in error and the process must live on.
func TestProcessPanicMarksItemFailed(t *testing.T) {
	svc, _, q, uid, _ := bootstrap(t, 0)
	svc.Client = &http.Client{Transport: panicTransport{}}
	item, err := svc.Create(context.Background(), uid, "https://example.com/boom", KindArticle, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, q, item.ID, uid, StatusError)
	if got.ErrorMsg == "" || strings.Contains(got.ErrorMsg, "parser blew up") {
		t.Fatalf("error message %q: want a generic message, not the panic value", got.ErrorMsg)
	}
}

// truncate cut error messages mid-rune; Postgres refused the invalid UTF-8, so
// SetSavedItemError failed and the item stayed in processing for good.
func TestTruncateKeepsUTF8Valid(t *testing.T) {
	msg := strings.Repeat("я", 400) // 800 bytes, 2 per rune
	for _, n := range []int{499, 500, 1, 0} {
		out := truncate("x"+msg, n)
		if !utf8.ValidString(out) || len(out) > n {
			t.Fatalf("truncate(%d) = %d bytes, valid=%v", n, len(out), utf8.ValidString(out))
		}
	}
	if out := truncate("ok\xffbad", 500); !utf8.ValidString(out) {
		t.Fatalf("invalid input must come out valid: %q", out)
	}
}

func TestLongCyrillicErrorIsRecorded(t *testing.T) {
	svc, _, q, uid, _ := bootstrap(t, 0)
	ctx := context.Background()
	item, err := q.UpsertSavedItem(ctx, db.UpsertSavedItemParams{UserID: uid, Url: "https://example.com/x", Kind: KindDownload})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := q.ClaimSavedItem(ctx, item.ID); n != 1 {
		t.Fatal("claim")
	}
	svc.setError(item, "x"+strings.Repeat("ошибка ", 100))
	waitStatus(t, q, item.ID, uid, StatusError)
}
