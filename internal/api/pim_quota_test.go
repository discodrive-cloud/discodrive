package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"discodrive/internal/quota"
)

// A calendar, task or contact write refused by the quota is "insufficient storage" for the
// client, not a server error it may retry forever.
func TestPutCalErrorQuotaIs507(t *testing.T) {
	rec := httptest.NewRecorder()
	writePutCalError(rec, fmt.Errorf("put: %w", quota.ErrExceeded))
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status %d, want 507", rec.Code)
	}
}

// An import that runs into the quota stops there and says so, instead of attempting (and
// skipping) every remaining card.
func TestImportStopsAtQuota(t *testing.T) {
	calls := 0
	put := func(uid, data string) error {
		calls++
		if calls > 1 {
			return quota.ErrExceeded
		}
		return nil
	}
	raw := ""
	for i := range 5 {
		raw += fmt.Sprintf("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:N%d\r\nUID:u%d\r\nEND:VCARD\r\n", i, i)
	}
	imported, _, err := importCards(raw, put)
	if !errors.Is(err, quota.ErrExceeded) || imported != 1 || calls != 2 {
		t.Fatalf("imported=%d calls=%d err=%v, want 1, 2, ErrExceeded", imported, calls, err)
	}
}
