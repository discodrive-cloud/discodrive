package dav_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/quota"
)

// bigEvent is a valid calendar object of roughly n bytes (the padding is a long DESCRIPTION).
func bigEvent(uid string, n int) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTART:20260612T120000Z\r\nDESCRIPTION:" + strings.Repeat("x", n) +
		"\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func bigCard(uid string, n int) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:A\r\nUID:" + uid + "\r\nNOTE:" + strings.Repeat("x", n) + "\r\nEND:VCARD\r\n"
}

// Calendar and contact objects live in Postgres, not in the file tree, but they are the
// user's data all the same: without a quota a user could park gigabytes in event
// descriptions.
func TestPIMObjectsCountAgainstQuota(t *testing.T) {
	ctx := context.Background()
	svc, q, userID := setup(t)
	uid, _ := db.ParseUUID(userID)
	if _, err := q.SetUserQuota(ctx, db.SetUserQuotaParams{ID: uid, StorageQuota: pgtype.Int8{Int64: 1 << 20, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	svc.SetQuota(quota.New(q, 0))

	cal, _ := svc.CreateCalendar(ctx, userID, "c", "")
	calID := db.UUIDString(cal.ID)
	if _, err := svc.PutCalendarObject(ctx, calID, "a", bigEvent("a", 600<<10)); err != nil {
		t.Fatalf("first event within quota: %v", err)
	}
	used, _ := q.UserStorageUsage(ctx, uid)
	if used < 600<<10 {
		t.Fatalf("usage %d does not count the stored event", used)
	}
	if _, err := svc.PutCalendarObject(ctx, calID, "b", bigEvent("b", 600<<10)); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("second event over quota: err = %v, want ErrExceeded", err)
	}
	// Rewriting the same object at the same size is not new data.
	if _, err := svc.PutCalendarObject(ctx, calID, "a", bigEvent("a", 600<<10-1)); err != nil {
		t.Fatalf("rewrite in place: %v", err)
	}

	ab, _ := svc.CreateAddressbook(ctx, userID, "ab")
	if _, err := svc.PutAddressbookObject(ctx, db.UUIDString(ab.ID), "c", bigCard("c", 600<<10)); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("card over quota: err = %v, want ErrExceeded", err)
	}
}
