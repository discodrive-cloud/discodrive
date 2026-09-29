package dav_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/dav"
	"discodrive/internal/db"
)

func setupShares(t *testing.T) (*dav.Service, *db.Queries, string, string) {
	t.Helper()
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("Docker required: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, _ := pgxpool.New(ctx, dsn)
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	owner, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "owner@x", PasswordHash: "x", Role: "user"})
	grantee, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "grantee@x", PasswordHash: "x", Role: "user"})
	return dav.NewService(pool), q, db.UUIDString(owner.ID), db.UUIDString(grantee.ID)
}

func TestShareCalendarFlow(t *testing.T) {
	ctx := context.Background()
	svc, _, ownerID, granteeID := setupShares(t)
	cal, err := svc.CreateCalendar(ctx, ownerID, "Семейный", "")
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	calID := db.UUIDString(cal.ID)

	// access before sharing
	if ok, _ := svc.CanAccessCalendar(ctx, ownerID, calID); !ok {
		t.Fatal("owner must have access")
	}
	if ok, _ := svc.CanAccessCalendar(ctx, granteeID, calID); ok {
		t.Fatal("recipient must not have access before sharing")
	}

	// share
	sh, err := svc.ShareCalendar(ctx, ownerID, calID, "grantee@x", nil)
	if err != nil {
		t.Fatalf("ShareCalendar: %v", err)
	}
	if ok, _ := svc.CanAccessCalendar(ctx, granteeID, calID); !ok {
		t.Fatal("recipient must have access after sharing")
	}

	// list of calendars shared with the grantee
	shared, err := svc.SharedCalendarsForUser(ctx, granteeID)
	if err != nil || len(shared) != 1 || db.UUIDString(shared[0].Calendar.ID) != calID || shared[0].OwnerEmail != "owner@x" {
		t.Fatalf("SharedCalendarsForUser: %+v err=%v", shared, err)
	}

	// list of shares from the owner's perspective
	infos, err := svc.ListCalendarShares(ctx, ownerID, calID)
	if err != nil || len(infos) != 1 || infos[0].Email != "grantee@x" {
		t.Fatalf("ListCalendarShares: %+v err=%v", infos, err)
	}

	// revoke
	if err := svc.DeleteCalendarShare(ctx, ownerID, db.UUIDString(sh.ID)); err != nil {
		t.Fatalf("DeleteCalendarShare: %v", err)
	}
	if ok, _ := svc.CanAccessCalendar(ctx, granteeID, calID); ok {
		t.Fatal("there must be no access after revocation")
	}
}

func TestShareAddressbookFlow(t *testing.T) {
	ctx := context.Background()
	svc, _, ownerID, granteeID := setupShares(t)
	ab, err := svc.CreateAddressbook(ctx, ownerID, "Семейная книга")
	if err != nil {
		t.Fatalf("CreateAddressbook: %v", err)
	}
	abID := db.UUIDString(ab.ID)

	if ok, _ := svc.CanAccessAddressbook(ctx, granteeID, abID); ok {
		t.Fatal("recipient must not have access before sharing")
	}
	sh, err := svc.ShareAddressbook(ctx, ownerID, abID, "grantee@x")
	if err != nil {
		t.Fatalf("ShareAddressbook: %v", err)
	}
	if ok, _ := svc.CanAccessAddressbook(ctx, granteeID, abID); !ok {
		t.Fatal("recipient must have access after sharing")
	}
	shared, err := svc.SharedAddressbooksForUser(ctx, granteeID)
	if err != nil || len(shared) != 1 || db.UUIDString(shared[0].Addressbook.ID) != abID || shared[0].OwnerEmail != "owner@x" {
		t.Fatalf("SharedAddressbooksForUser: %+v err=%v", shared, err)
	}
	infos, err := svc.ListAddressbookShares(ctx, ownerID, abID)
	if err != nil || len(infos) != 1 || infos[0].Email != "grantee@x" {
		t.Fatalf("ListAddressbookShares: %+v err=%v", infos, err)
	}
	if err := svc.DeleteAddressbookShare(ctx, ownerID, db.UUIDString(sh.ID)); err != nil {
		t.Fatalf("DeleteAddressbookShare: %v", err)
	}
	if ok, _ := svc.CanAccessAddressbook(ctx, granteeID, abID); ok {
		t.Fatal("there must be no access after revocation")
	}
}

func TestShareAddressbookErrors(t *testing.T) {
	ctx := context.Background()
	svc, _, ownerID, granteeID := setupShares(t)
	ab, _ := svc.CreateAddressbook(ctx, ownerID, "X")
	abID := db.UUIDString(ab.ID)
	if _, err := svc.ShareAddressbook(ctx, granteeID, abID, "owner@x"); err != dav.ErrNotOwner {
		t.Fatalf("expected ErrNotOwner, got %v", err)
	}
	if _, err := svc.ShareAddressbook(ctx, ownerID, abID, "nobody@x"); !errors.Is(err, dav.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestShareCalendarErrors(t *testing.T) {
	ctx := context.Background()
	svc, _, ownerID, granteeID := setupShares(t)
	cal, _ := svc.CreateCalendar(ctx, ownerID, "X", "")
	calID := db.UUIDString(cal.ID)
	// not the owner
	if _, err := svc.ShareCalendar(ctx, granteeID, calID, "owner@x", nil); err != dav.ErrNotOwner {
		t.Fatalf("expected ErrNotOwner, got %v", err)
	}
	// unknown email
	if _, err := svc.ShareCalendar(ctx, ownerID, calID, "nobody@x", nil); !errors.Is(err, dav.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCalendarFeedLink(t *testing.T) {
	ctx := context.Background()
	svc, _, ownerID, otherID := setupShares(t)
	cal, _ := svc.CreateCalendar(ctx, ownerID, "Публичный", "")
	calID := db.UUIDString(cal.ID)

	// without password
	tok, err := svc.CreateCalendarFeedLink(ctx, ownerID, calID, "")
	if err != nil || tok == "" {
		t.Fatalf("CreateCalendarFeedLink: tok=%q err=%v", tok, err)
	}
	gotCal, hash, ok := svc.CalendarByFeedToken(ctx, tok)
	if !ok || gotCal != calID || hash != "" {
		t.Fatalf("CalendarByFeedToken: cal=%q hash=%q ok=%v", gotCal, hash, ok)
	}

	// with password (hash is computed by the caller; here just a non-empty "hash" string)
	tok2, err := svc.CreateCalendarFeedLink(ctx, ownerID, calID, "HASHVALUE")
	if err != nil {
		t.Fatalf("CreateCalendarFeedLink(pw): %v", err)
	}
	_, hash2, ok2 := svc.CalendarByFeedToken(ctx, tok2)
	if !ok2 || hash2 != "HASHVALUE" {
		t.Fatalf("expected hash=HASHVALUE, got %q ok=%v", hash2, ok2)
	}

	// list (2 links, second one has_password)
	links, err := svc.ListCalendarFeedLinks(ctx, ownerID, calID)
	if err != nil || len(links) != 2 {
		t.Fatalf("ListCalendarFeedLinks: %+v err=%v", links, err)
	}
	withPw := 0
	for _, l := range links {
		if l.HasPassword {
			withPw++
		}
	}
	if withPw != 1 {
		t.Fatalf("expected 1 password-protected link, got %d", withPw)
	}

	// non-owner cannot create a feed link
	if _, err := svc.CreateCalendarFeedLink(ctx, otherID, calID, ""); err != dav.ErrNotOwner {
		t.Fatalf("expected ErrNotOwner, got %v", err)
	}

	// unknown token
	if _, _, ok := svc.CalendarByFeedToken(ctx, "nope"); ok {
		t.Fatal("unknown token must not resolve")
	}
}
