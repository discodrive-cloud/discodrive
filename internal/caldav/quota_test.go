package caldav_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/quota"
)

// A PUT that does not fit the owner's quota is 507, as for files over WebDAV.
func TestPutOverQuotaIs507(t *testing.T) {
	e := setupReview(t)
	q := db.New(e.pool)
	uid, _ := db.ParseUUID(e.ownerID)
	if _, err := q.SetUserQuota(context.Background(), db.SetUserQuotaParams{ID: uid, StorageQuota: pgtype.Int8{Int64: 1024, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	e.svc.SetQuota(quota.New(q, 0))
	rec := e.req(t, e.ownerID, http.MethodPut, e.calDir+"big.ics", eventWith("big", strings.Repeat("x", 4096)), nil)
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("PUT over quota: %d %s, want 507", rec.Code, rec.Body.String())
	}
}
