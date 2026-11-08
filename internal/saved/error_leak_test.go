package saved

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discodrive/internal/fetchguard"
)

// The SSRF guard's errors name the internal host and the address it resolved to. The
// item's error is shown to the user, so it must not carry them: otherwise a public URL
// that redirects inward turns Saved into a probe of the server's internal DNS.
func TestBlockedFetchErrorHidesInternalAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:6379/x", http.StatusFound)
	}))
	defer srv.Close()

	svc, _, q, uid, _ := bootstrap(t, 0)
	// The first hop is a loopback test server, so only redirects get the real guard.
	svc.Client = &http.Client{CheckRedirect: fetchguard.NewClient(0).CheckRedirect}
	for _, kind := range []string{KindDownload, KindArticle} {
		item, err := svc.Create(context.Background(), uid, srv.URL+"/r", kind, "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		got := waitStatus(t, q, item.ID, uid, StatusError)
		for _, leak := range []string{"localhost", "::1", "127.0.0.1", "6379"} {
			if strings.Contains(got.ErrorMsg, leak) {
				t.Fatalf("%s: error shown to the user leaks %q: %q", kind, leak, got.ErrorMsg)
			}
		}
		if got.ErrorMsg == "" {
			t.Fatalf("%s: empty error", kind)
		}
	}
}
