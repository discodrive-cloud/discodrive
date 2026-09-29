package opds

import (
	"net/http"
	"testing"
)

// Wrong Basic credentials are cut off after the budget; the anonymous request
// that fetches the challenge and a correctly authenticated reader never are.
func TestAuthFailuresAreRateLimited(t *testing.T) {
	h, _ := setupOPDS(t)

	for i := 0; i < 30; i++ {
		if rec := opdsGet(h, "/opds"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %d: %d, want 401 challenge", i, rec.Code)
		}
		if rec := opdsGetBasic(h, "/opds", testEmail, testPassword); rec.Code != http.StatusOK {
			t.Fatalf("valid %d: %d", i, rec.Code)
		}
	}
	for i := 0; i < 10; i++ {
		if rec := opdsGetBasic(h, "/opds", testEmail, "guess"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d, want 401", i, rec.Code)
		}
	}
	rec := opdsGetBasic(h, "/opds", testEmail, testPassword)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after the budget: %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
}
