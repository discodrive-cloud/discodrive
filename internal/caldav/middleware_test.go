package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"discodrive/internal/db"
)

type fakeSettings struct{ enabled bool }

func (f fakeSettings) GetSetting(_ context.Context, key string) (db.Setting, error) {
	if key == "caldav.enabled" && f.enabled {
		return db.Setting{Key: key, Value: "true"}, nil
	}
	return db.Setting{Value: "false"}, nil
}

func TestHandlerForbiddenWhenDisabled(t *testing.T) {
	h := Handler(nil, fakeSettings{enabled: false}, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PROPFIND", "/caldav/", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when CalDAV is disabled, got %d", rec.Code)
	}
}

func TestWellKnownRedirects(t *testing.T) {
	rec := httptest.NewRecorder()
	WellKnown().ServeHTTP(rec, httptest.NewRequest("PROPFIND", "/.well-known/caldav", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/caldav/" {
		t.Fatalf("well-known: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAsPrincipalRewritesOnlyRoot(t *testing.T) {
	root := asPrincipal(httptest.NewRequest("PROPFIND", "/", nil), "u1")
	if root.URL.Path != principalPath("u1") || root.RequestURI != principalPath("u1") {
		t.Fatalf("root was served at %q (RequestURI %q), want the principal", root.URL.Path, root.RequestURI)
	}
	other := httptest.NewRequest("PROPFIND", prefix+"/u1/", nil)
	if got := asPrincipal(other, "u1"); got != other {
		t.Fatalf("non-root request must pass through unchanged, got %q", got.URL.Path)
	}
}
