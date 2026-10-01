package api

import (
	"context"
	"discodrive/internal/db"
	"github.com/google/uuid"
	"testing"
)

func TestRegressionBulkChangesFolderType(t *testing.T) {
	h := newBookmarkHarness(t)
	p, c := uuid.NewString(), uuid.NewString()
	if r, _ := h.bulk(t, bulkItem(p, "", true, ""), bulkItem(c, p, false, "https://example.com")); r.Code != 200 {
		t.Fatal(r.Body)
	}
	r, _ := h.bulk(t, bulkItem(p, "", false, "https://example.org"))
	if r.Code != 400 {
		t.Fatalf("invalid folder conversion accepted: %d", r.Code)
	}
	rows, e := h.q.ListBrowserBookmarks(context.Background(), h.uid)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 2 {
		t.Fatalf("tree changed after refused import: %+v", rows)
	}
	for _, row := range rows {
		if db.UUIDString(row.ID) == p && !row.IsFolder {
			t.Fatal("folder type changed after rollback")
		}
	}
}
