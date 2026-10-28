package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSectionGate(t *testing.T) {
	s := &Server{sections: Sections{Music: false, Books: true, Photos: false}}
	h := s.sectionGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // reached the real route
	}))
	cases := []struct {
		method, path string
		code         int
	}{
		{"GET", "/me/music", 200}, // says it is off
		{"PUT", "/me/music", 404}, // cannot be turned on
		{"POST", "/me/music/scan", 404},
		{"GET", "/rest/ping.view", 404},
		{"GET", "/me/musicals", http.StatusTeapot}, // not the section, only a prefix of its name
		{"GET", "/me/ebooks", http.StatusTeapot},   // books stay on
		{"GET", "/opds/", http.StatusTeapot},
		{"GET", "/me/photos/v1/settings", 200},
		{"PUT", "/me/photos/v1/settings", 404},
		{"GET", "/me/photos/v1/buckets", 404},
		{"GET", "/admin/photos/v1/maps", 404},
		{"GET", "/files", http.StatusTeapot},
		{"GET", "/me/features", http.StatusTeapot},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/me/photos/v1/settings", nil))
	var st struct{ Available, Enabled, Disabled bool }
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.Available || st.Enabled || !st.Disabled {
		t.Fatalf("photos settings when off: %s", rec.Body)
	}

	// Books off: OPDS and KOReader sync go with them.
	s.sections = Sections{Music: true, Books: false, Photos: true}
	h = s.sectionGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, p := range []string{"/opds", "/opds/books", "/users/auth", "/syncs/progress/x", "/me/ebooks/library"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 404 {
			t.Errorf("books off: GET %s = %d", p, rec.Code)
		}
	}
}

func TestFeatures(t *testing.T) {
	s := &Server{sections: Sections{Music: true, Books: false, Photos: true}}
	rec := httptest.NewRecorder()
	s.handleGetFeatures(rec, httptest.NewRequest("GET", "/me/features", nil))
	if rec.Body.String() != `{"music":true,"books":false,"photos":true}`+"\n" {
		t.Fatalf("features = %q", rec.Body)
	}
}
