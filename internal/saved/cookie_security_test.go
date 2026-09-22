package saved

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive"
	"discodrive/internal/db"
	"discodrive/internal/secret"
)

func TestCookieEncryptionBindingAndExpiry(t *testing.T) {
	cipher, _ := secret.New("0123456789abcdef0123456789abcdef")
	svc := &Service{cipher: cipher}
	uid, _ := db.ParseUUID("11111111-1111-1111-1111-111111111111")
	encrypted, err := svc.sealCookie(uid, "https://example.test/file", "session=secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encrypted, "session=secret") {
		t.Fatal("plaintext stored")
	}
	item := db.SavedItem{UserID: uid, Url: "https://example.test/file", CookieHeader: pgtype.Text{String: encrypted, Valid: true}, CookieExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}}
	if got, err := svc.openCookie(item); err != nil || got != "session=secret" {
		t.Fatalf("decrypt: %q %v", got, err)
	}
	other := item
	other.Url = "https://attacker.test/file"
	if _, err := svc.openCookie(other); err == nil {
		t.Fatal("cookie transferable between URLs")
	}
	other = item
	other.CookieExpiresAt.Time = time.Now().Add(-time.Minute)
	if _, err := svc.openCookie(other); err == nil {
		t.Fatal("expired cookie accepted")
	}
	other = item
	other.CookieHeader.String = "session=secret"
	if _, err := svc.openCookie(other); err == nil {
		t.Fatal("legacy plaintext accepted")
	}
	if _, err := svc.sealCookie(uid, "http://example.test/file", "session=secret"); err == nil {
		t.Fatal("HTTP accepted")
	}
	svc.cipher = nil
	if _, err := svc.sealCookie(uid, item.Url, "session=secret"); err == nil {
		t.Fatal("unencrypted credentials accepted")
	}
}

func TestSavedCookieLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, pool, q, uid, _ := bootstrap(t, 0)
	encrypted, err := svc.sealCookie(uid, "https://example.test/file", "session=secret")
	if err != nil {
		t.Fatal(err)
	}
	create := func(url string) db.SavedItem {
		t.Helper()
		item, err := q.UpsertSavedItem(ctx, db.UpsertSavedItemParams{UserID: uid, Url: url, Kind: KindDownload, CookieHeader: pgtype.Text{String: encrypted, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	checkCleared := func(id pgtype.UUID) {
		t.Helper()
		got, err := q.GetSavedItemForUser(ctx, db.GetSavedItemForUserParams{ID: id, UserID: uid})
		if err != nil {
			t.Fatal(err)
		}
		if got.CookieHeader.Valid {
			t.Fatal("cookie remains in DB")
		}
	}
	item := create("https://example.test/file")
	if !item.CookieExpiresAt.Valid {
		t.Fatal("no expiry")
	}
	if _, err := q.ClaimSavedItem(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	checkCleared(item.ID)
	if err := svc.RecoverStale(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetSavedItemForUser(ctx, db.GetSavedItemForUserParams{ID: item.ID, UserID: uid})
	if got.Status != StatusError {
		t.Fatal("credential-bearing job retried without credentials after crash")
	}
	item = create("https://example.test/error")
	if err := q.SetSavedItemError(ctx, db.SetSavedItemErrorParams{ID: item.ID, ErrorMsg: "failed"}); err != nil {
		t.Fatal(err)
	}
	checkCleared(item.ID)
	item = create("https://example.test/done")
	if _, err := pool.Exec(ctx, "UPDATE saved_items SET status='done',cookie_header=NULL WHERE id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	create(item.Url)
	checkCleared(item.ID)
	item = create("https://example.test/expired")
	if _, err := pool.Exec(ctx, "UPDATE saved_items SET cookie_expires_at=now()-interval '1 minute' WHERE id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	if err := q.ClearExpiredSavedCookies(ctx); err != nil {
		t.Fatal(err)
	}
	checkCleared(item.ID)
}

func TestCookieSecurityMigrationPurgesLegacySecrets(t *testing.T) {
	ctx := context.Background()
	_, pool, q, uid, _ := bootstrap(t, 0)
	item, err := q.UpsertSavedItem(ctx, db.UpsertSavedItemParams{UserID: uid, Url: "https://example.test/legacy", Kind: KindDownload})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "ALTER TABLE saved_items DROP COLUMN cookie_expires_at"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE saved_items SET cookie_header='session=legacy-secret',status='pending' WHERE id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	migration, err := discodrive.Migrations.ReadFile("migrations/000009_saved_cookie_security.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var status string
	var cookie *string
	if err = tx.QueryRow(ctx, "SELECT status,cookie_header FROM saved_items WHERE id=$1", item.ID).Scan(&status, &cookie); err != nil {
		t.Fatal(err)
	}
	if cookie != nil || status != StatusError {
		t.Fatal("legacy credentials survived migration or job was silently retried")
	}
}
