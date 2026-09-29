package kosync_test

import (
	"net/http"
	"testing"
)

// KOReader syncs on every page turn: valid keys are never throttled, wrong keys
// are cut off after the per-client budget.
func TestAuthFailuresAreRateLimited(t *testing.T) {
	h, _, _, _, _, _ := setupKosync(t)

	for i := 0; i < 40; i++ {
		if rec := doRequest(h, http.MethodGet, "/users/auth", testEmail1, md5Key(testPassword), nil); rec.Code != http.StatusOK {
			t.Fatalf("valid %d: %d", i, rec.Code)
		}
	}
	for i := 0; i < 10; i++ {
		if rec := doRequest(h, http.MethodGet, "/users/auth", testEmail1, md5Key("guess"), nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d, want 401", i, rec.Code)
		}
	}
	rec := doRequest(h, http.MethodGet, "/users/auth", testEmail1, md5Key(testPassword), nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after the budget: %d, want 429", rec.Code)
	}
}
