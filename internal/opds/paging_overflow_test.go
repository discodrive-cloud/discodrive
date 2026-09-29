package opds

import (
	"net/http"
	"testing"
)

// A ?start= past int32 used to wrap negative and fail the query with 500.
func TestStartOffsetDoesNotOverflowInt32(t *testing.T) {
	h, _ := setupOPDS(t)
	seedAcqBooks(t, h)
	for _, target := range []string{
		"/opds/all?start=2147483648",
		"/opds/new?start=2147483648",
		"/opds/search?q=Book&start=2147483648",
		"/opds/all?start=99999999999999999999",
	} {
		if rec := opdsGetBasic(h, target, testEmail, testPassword); rec.Code != http.StatusOK {
			t.Errorf("%s: %d, want 200", target, rec.Code)
		}
	}
}
