package opds

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestRegressionAPIKeyNavigation(t *testing.T) {
	h, _ := setupOPDS(t)
	req := httptest.NewRequest("GET", "/opds?apiKey="+testAPIKey, nil)
	req.Header.Set("Accept", "application/opds+json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	var f feed2
	if e := json.Unmarshal(rec.Body.Bytes(), &f); e != nil {
		t.Fatal(e)
	}
	next := f.Navigation[0].Href
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", next, nil))
	t.Logf("root200 next=%s status=%d", next, rec.Code)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}
