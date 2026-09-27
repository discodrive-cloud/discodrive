package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIBodyLimits(t *testing.T) {
	for _, tc := range []struct {
		pattern, path, contentType string
		size                       int
		chunked                    bool
		want                       int
	}{
		{"POST /auth/login", "/auth/login", "application/json", maxAuthBody + 1, false, 413},
		{"POST /auth/register", "/auth/register", "application/octet-stream", maxAuthBody + 1, true, 413},
		{"POST /auth/mfa/totp", "/auth/mfa/totp", "", maxAuthBody + 1, true, 413},
		{"POST /setup/admin", "/setup/admin", "", maxAuthBody + 1, true, 413},
		{"PUT /me/password", "/me/password", "", maxAuthBody + 1, true, 413},
		{"PUT /me/music", "/me/music", "text/plain", maxJSONBody + 1, true, 413},
		{"POST /auth/login", "/auth/login", "application/json", maxAuthBody, true, 204},
		{"PUT /me/music", "/me/music", "application/json", maxJSONBody, false, 204},
		{"POST /files/upload", "/files/upload", "multipart/form-data", maxJSONBody + 1, true, 204},
		{"PUT /sync/file", "/sync/file", "application/json", maxJSONBody + 1, true, 204},
		{"PUT /upload/{id}/chunk/{n}", "/upload/id/chunk/0", "", maxJSONBody + 1, true, 204},
		{"/dav/", "/dav/file", "", maxJSONBody + 1, true, 204},
		{"POST /me/contacts/import", "/me/contacts/import", "text/vcard", maxJSONBody + 1, true, 204},
	} {
		t.Run(tc.path+tc.contentType, func(t *testing.T) {
			called := false
			mux := http.NewServeMux()
			mux.HandleFunc(tc.pattern, func(w http.ResponseWriter, r *http.Request) {
				called = true
				n, err := io.Copy(io.Discard, r.Body)
				if err != nil || int(n) != tc.size {
					t.Fatalf("body changed n=%d err=%v", n, err)
				}
				w.WriteHeader(204)
			})
			method := "POST"
			if strings.HasPrefix(tc.pattern, "PUT ") {
				method = "PUT"
			}
			r := httptest.NewRequest(method, tc.path, strings.NewReader(strings.Repeat("x", tc.size)))
			r.Header.Set("Content-Type", tc.contentType)
			if tc.chunked {
				r.ContentLength = -1
			}
			w := httptest.NewRecorder()
			limitAPIBodies(mux).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
			if called != (tc.want == 204) {
				t.Fatal("oversized body reached handler")
			}
		})
	}
}

func TestPublicAuthRoutesRejectOversizeBeforeServices(t *testing.T) {
	// Real router with no services: oversized requests must be rejected before
	// login, registration, or any database/password work is invoked.
	h := NewRouter(nil, nil, nil, nil, "", nil, nil, nil, nil, nil, nil, nil, false, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, path := range []string{"/auth/login", "/auth/register", "/auth/mfa/totp", "/auth/device/token", "/auth/webauthn/login/finish", "/setup/admin"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(strings.Repeat("x", maxAuthBody+1)))
		r.ContentLength = -1
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 413 {
			t.Fatalf("%s=%d", path, w.Code)
		}
	}
}
