package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discodrive/internal/secret"
)

// The SMTP form is several settings that only work together. Sent in one request they
// are saved together or not at all.
func TestAdminSettingsAreSavedTogether(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	tok, _, err := svc.Register(ctx, "admin@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	noKey, err := secret.New("") // no encryption key: a secret cannot be stored
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: svc, q: q, cipher: noKey}
	put := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		svc.Middleware(http.HandlerFunc(s.handleAdminPutSetting)).ServeHTTP(rec, r)
		return rec
	}
	value := func(key string) string {
		row, err := q.GetSetting(ctx, key)
		if err != nil {
			return ""
		}
		return row.Value
	}

	if rec := put(`{"settings":[{"key":"smtp.host","value":"old.example"},{"key":"smtp.port","value":"25"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	if value("smtp.host") != "old.example" || value("smtp.port") != "25" {
		t.Fatalf("batch not saved: host %q port %q", value("smtp.host"), value("smtp.port"))
	}
	// The password cannot be stored here, so the new host must not be either.
	if rec := put(`{"settings":[{"key":"smtp.host","value":"new.example"},{"key":"smtp.password","value":"s3cret"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("batch with an unstorable secret: %d, want 400", rec.Code)
	}
	if got := value("smtp.host"); got != "old.example" {
		t.Fatalf("a refused batch changed the host to %q", got)
	}
	// One setting at a time still works.
	if rec := put(`{"key":"smtp.port","value":"587"}`); rec.Code != http.StatusOK || value("smtp.port") != "587" {
		t.Fatalf("single: %d, port %q", rec.Code, value("smtp.port"))
	}
	for _, bad := range []string{`{}`, `{"settings":[]}`, `{"settings":[{"key":"","value":"x"}]}`, `{"key":"a","value":"b","settings":[{"key":"c","value":"d"}]}`} {
		if rec := put(bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}
}
