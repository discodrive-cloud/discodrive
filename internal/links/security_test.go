package links

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"discodrive/internal/fetchguard"
)

// Links pointing inside the server's network are refused before anything is stored.
func TestSaveRefusesInternalAddresses(t *testing.T) {
	_, q, uid := bootstrap(t)
	s := NewService(q) // the real SSRF guard
	for _, raw := range []string{
		"http://127.0.0.1/", "http://10.0.0.1/admin", "http://100.64.0.1/", "http://[::1]/",
		"http://169.254.169.254/latest/meta-data/", "http://192.168.1.1/", "http://localhost:8080/",
	} {
		if _, err := s.Save(context.Background(), uid, raw, ""); !errors.Is(err, fetchguard.ErrBlocked) {
			t.Errorf("Save(%q) err = %v, want ErrBlocked", raw, err)
		}
	}
	if p, _ := s.List(context.Background(), uid, false, ""); len(p.Items) != 0 {
		t.Fatalf("refused links must not be stored: %+v", p.Items)
	}
}

// A host that does not resolve right now is kept: losing a shared link is worse.
func TestSaveKeepsUnresolvedHosts(t *testing.T) {
	_, q, uid := bootstrap(t)
	s := NewService(q)
	s.noFetch = true
	if _, err := s.Save(context.Background(), uid, "https://does-not-exist.invalid/post", ""); err != nil {
		t.Fatalf("unresolved host must be accepted, got %v", err)
	}
}

// The default client's dial-time check stops the fetch even when validation is
// bypassed (DNS rebinding): nothing is read from an internal address.
func TestEnrichUsesTheGuardedClient(t *testing.T) {
	_, q, uid := bootstrap(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		page(`<meta property="og:title" content="internal secret">`)(w, r)
	}))
	defer srv.Close()
	s := NewService(q)
	s.Validate = func(string) error { return nil } // pretend the host passed validation
	if _, err := s.Save(context.Background(), uid, srv.URL+"/", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	p, _ := s.List(context.Background(), uid, false, "")
	if hits.Load() != 0 || p.Items[0].Title != "" || !p.Items[0].enriched {
		t.Fatalf("internal page was fetched: hits=%d %+v", hits.Load(), p.Items[0])
	}
}

// A redirect to an internal address is not followed.
func TestEnrichRefusesRedirectInside(t *testing.T) {
	_, q, uid := bootstrap(t)
	var inside atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inside.Add(1)
		page(`<title>secret</title>`)(w, r)
	}))
	defer internal.Close()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/", http.StatusFound)
	}))
	defer public.Close()
	s := NewService(q)
	s.Client = &http.Client{CheckRedirect: s.Client.CheckRedirect} // real redirect policy, plain dialer
	s.Validate = func(u string) error {
		if strings.HasPrefix(u, internal.URL) {
			return fetchguard.ErrBlocked
		}
		return nil
	}
	// The redirect policy calls fetchguard.ValidateURL, which refuses 127.0.0.1 itself.
	if _, err := s.Save(context.Background(), uid, public.URL+"/", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if inside.Load() != 0 {
		t.Fatal("redirect to an internal address was followed")
	}
}

// og:image pointing inside the network is never fetched.
func TestEnrichRefusesInternalPreview(t *testing.T) {
	_, q, uid := bootstrap(t)
	srv := httptest.NewServer(page(`<title>ok</title><meta property="og:image" content="http://169.254.169.254/x.png">`))
	defer srv.Close()
	s := NewService(q)
	s.Client = &http.Client{}
	s.Validate = func(u string) error {
		if strings.HasPrefix(u, srv.URL) {
			return nil
		}
		return fetchguard.ValidateURL(u)
	}
	if _, err := s.Save(context.Background(), uid, srv.URL+"/", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	p, _ := s.List(context.Background(), uid, false, "")
	if l := p.Items[0]; l.Title != "ok" || l.HasThumb {
		t.Fatalf("got %+v", l)
	}
}

// A page serving HTML or SVG as an image yields no preview.
func TestEnrichRejectsDisguisedPreview(t *testing.T) {
	_, q, uid := bootstrap(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/fake.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	})
	mux.HandleFunc("/", page(`<meta property="og:image" content="/fake.png">`))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := testService(q)
	if _, err := s.Save(context.Background(), uid, srv.URL+"/", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	p, _ := s.List(context.Background(), uid, false, "")
	if p.Items[0].HasThumb {
		t.Fatal("disguised SVG became a preview")
	}
}

// data: and javascript: previews are not fetched or decoded.
func TestEnrichIgnoresNonWebPreview(t *testing.T) {
	_, q, uid := bootstrap(t)
	srv := httptest.NewServer(page(`<meta property="og:image" content="data:image/png;base64,iVBORw0KGgo=">`))
	defer srv.Close()
	s := testService(q)
	if _, err := s.Save(context.Background(), uid, srv.URL+"/", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	p, _ := s.List(context.Background(), uid, false, "")
	if p.Items[0].HasThumb {
		t.Fatal("data: preview must be ignored")
	}
}
