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

func setupReview(t *testing.T) (*dav.Service, *pgxpool.Pool, string, string) {
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
	return dav.NewService(pool), pool, db.UUIDString(owner.ID), db.UUIDString(grantee.ID)
}

// Sharing twice with the same user used to add a second share row, and the calendar showed
// up twice in the recipient's home set; sharing with yourself put it twice in your own.
func TestShareTwiceKeepsOneShare(t *testing.T) {
	ctx := context.Background()
	svc, _, ownerID, granteeID := setupReview(t)
	cal, _ := svc.CreateCalendar(ctx, ownerID, "Семейный", "")
	calID := db.UUIDString(cal.ID)
	ab, _ := svc.CreateAddressbook(ctx, ownerID, "Книга")
	abID := db.UUIDString(ab.ID)

	for i := 0; i < 2; i++ {
		if _, err := svc.ShareCalendar(ctx, ownerID, calID, "grantee@x", nil); err != nil {
			t.Fatalf("ShareCalendar #%d: %v", i+1, err)
		}
		if _, err := svc.ShareAddressbook(ctx, ownerID, abID, "grantee@x"); err != nil {
			t.Fatalf("ShareAddressbook #%d: %v", i+1, err)
		}
	}
	if shares, _ := svc.ListCalendarShares(ctx, ownerID, calID); len(shares) != 1 {
		t.Fatalf("calendar shares after sharing twice: %d, want 1", len(shares))
	}
	if cals, _ := svc.SharedCalendarsForUser(ctx, granteeID); len(cals) != 1 {
		t.Fatalf("recipient sees the calendar %d times, want once", len(cals))
	}
	if shares, _ := svc.ListAddressbookShares(ctx, ownerID, abID); len(shares) != 1 {
		t.Fatalf("address book shares after sharing twice: %d, want 1", len(shares))
	}
	if abs, _ := svc.SharedAddressbooksForUser(ctx, granteeID); len(abs) != 1 {
		t.Fatalf("recipient sees the address book %d times, want once", len(abs))
	}

	if _, err := svc.ShareCalendar(ctx, ownerID, calID, "owner@x", nil); !errors.Is(err, dav.ErrSelfShare) {
		t.Fatalf("self-share of a calendar: %v, want ErrSelfShare", err)
	}
	if _, err := svc.ShareAddressbook(ctx, ownerID, abID, "owner@x"); !errors.Is(err, dav.ErrSelfShare) {
		t.Fatalf("self-share of an address book: %v, want ErrSelfShare", err)
	}
	if cals, _ := svc.SharedCalendarsForUser(ctx, ownerID); len(cals) != 0 {
		t.Fatalf("owner sees their own calendar as shared: %d", len(cals))
	}
	if _, err := svc.ShareCalendar(ctx, ownerID, calID, "nobody@x", nil); !errors.Is(err, dav.ErrRecipientNotFound) {
		t.Fatalf("unknown recipient: %v, want ErrRecipientNotFound", err)
	}
}

// The recipient can leave a share; a third user cannot remove it.
func TestRecipientCanLeaveShare(t *testing.T) {
	ctx := context.Background()
	svc, _, ownerID, granteeID := setupReview(t)
	cal, _ := svc.CreateCalendar(ctx, ownerID, "Семейный", "")
	calID := db.UUIDString(cal.ID)
	ab, _ := svc.CreateAddressbook(ctx, ownerID, "Книга")

	if _, err := svc.ShareCalendar(ctx, ownerID, calID, "grantee@x", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ShareAddressbook(ctx, ownerID, db.UUIDString(ab.ID), "grantee@x"); err != nil {
		t.Fatal(err)
	}
	cals, _ := svc.SharedCalendarsForUser(ctx, granteeID)
	abs, _ := svc.SharedAddressbooksForUser(ctx, granteeID)
	if len(cals) != 1 || cals[0].ShareID == "" || len(abs) != 1 || abs[0].ShareID == "" {
		t.Fatalf("shared lists: %+v %+v", cals, abs)
	}
	// the address book share is not a calendar share
	if err := svc.DeleteCalendarShare(ctx, granteeID, abs[0].ShareID); !errors.Is(err, dav.ErrNotFound) {
		t.Fatalf("DeleteCalendarShare with an address book share: %v", err)
	}
	if err := svc.DeleteCalendarShare(ctx, granteeID, cals[0].ShareID); err != nil {
		t.Fatalf("recipient leaves the calendar: %v", err)
	}
	if err := svc.DeleteAddressbookShare(ctx, granteeID, abs[0].ShareID); err != nil {
		t.Fatalf("recipient leaves the address book: %v", err)
	}
	if ok, _ := svc.CanAccessCalendar(ctx, granteeID, calID); ok {
		t.Fatal("recipient still has access after leaving")
	}
	// a feed link has no recipient: only the owner can revoke it
	if _, err := svc.CreateCalendarFeedLink(ctx, ownerID, calID, ""); err != nil {
		t.Fatal(err)
	}
	links, _ := svc.ListCalendarFeedLinks(ctx, ownerID, calID)
	if err := svc.DeleteCalendarShare(ctx, granteeID, links[0].ID); !errors.Is(err, dav.ErrNotOwner) {
		t.Fatalf("non-owner revoking a feed link: %v, want ErrNotOwner", err)
	}
}

// A password-protected feed link must never exist without its password. It used to be
// inserted open and protected by a second statement; a failure in between (simulated here
// by a trigger that rejects updates) left a public link to the calendar.
func TestFeedLinkWithPasswordIsCreatedAtomically(t *testing.T) {
	ctx := context.Background()
	svc, pool, ownerID, _ := setupReview(t)
	cal, _ := svc.CreateCalendar(ctx, ownerID, "Фид", "")
	calID := db.UUIDString(cal.ID)
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION no_share_update() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'update refused'; END $$;
		CREATE TRIGGER no_share_update BEFORE UPDATE ON resource_shares
		FOR EACH ROW EXECUTE FUNCTION no_share_update();`); err != nil {
		t.Fatal(err)
	}
	_, err := svc.CreateCalendarFeedLink(ctx, ownerID, calID, "argon2-hash")
	links, _ := svc.ListCalendarFeedLinks(ctx, ownerID, calID)
	for _, l := range links {
		if !l.HasPassword {
			t.Fatalf("an open feed link exists (create err: %v)", err)
		}
	}
	if err != nil || len(links) != 1 {
		t.Fatalf("create: %v, links: %+v", err, links)
	}
}

// Deleting a calendar deletes its shares and feed links: the feed of a deleted calendar used
// to keep answering with an empty calendar. A foreign or unknown id is ErrNotFound, a
// malformed one ErrBadID (the handler answered 204 and 500).
func TestDeleteCalendarRemovesSharesAndReportsMissing(t *testing.T) {
	ctx := context.Background()
	svc, _, ownerID, granteeID := setupReview(t)
	cal, _ := svc.CreateCalendar(ctx, ownerID, "Удаляемый", "")
	calID := db.UUIDString(cal.ID)
	tok, err := svc.CreateCalendarFeedLink(ctx, ownerID, calID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ShareCalendar(ctx, ownerID, calID, "grantee@x", nil); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteCalendar(ctx, granteeID, calID); !errors.Is(err, dav.ErrNotFound) {
		t.Fatalf("deleting someone else's calendar: %v, want ErrNotFound", err)
	}
	if err := svc.DeleteCalendar(ctx, ownerID, "not-a-uuid"); !errors.Is(err, dav.ErrBadID) {
		t.Fatalf("malformed id: %v, want ErrBadID", err)
	}
	if err := svc.DeleteCalendar(ctx, ownerID, calID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, ok := svc.CalendarByFeedToken(ctx, tok); ok {
		t.Fatal("the feed token of a deleted calendar still resolves")
	}
	if cals, _ := svc.SharedCalendarsForUser(ctx, granteeID); len(cals) != 0 {
		t.Fatalf("recipient still lists the deleted calendar: %+v", cals)
	}
	if err := svc.DeleteCalendar(ctx, ownerID, calID); !errors.Is(err, dav.ErrNotFound) {
		t.Fatalf("second delete: %v, want ErrNotFound", err)
	}

	ab, _ := svc.CreateAddressbook(ctx, ownerID, "Книга")
	abID := db.UUIDString(ab.ID)
	if _, err := svc.ShareAddressbook(ctx, ownerID, abID, "grantee@x"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteAddressbook(ctx, ownerID, abID); err != nil {
		t.Fatalf("delete address book: %v", err)
	}
	if abs, _ := svc.SharedAddressbooksForUser(ctx, granteeID); len(abs) != 0 {
		t.Fatalf("recipient still lists the deleted address book: %+v", abs)
	}
}
