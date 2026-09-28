package db_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"discodrive/internal/db"
)

func TestRescanRequestsQueueAndNotify(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "r@x", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN rescan_requests"); err != nil {
		t.Fatal(err)
	}

	req, err := q.CreateRescanRequest(ctx, db.CreateRescanRequestParams{UserID: u.ID, RequestedBy: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := conn.Conn().WaitForNotification(wctx)
	if err != nil || n.Payload != strconv.FormatInt(req.ID, 10) {
		t.Fatalf("notification %v %v, want payload %d", n, err, req.ID)
	}
	if pending, _ := q.ListPendingRescanRequests(ctx); len(pending) != 1 {
		t.Fatalf("pending %d, want 1", len(pending))
	}
	if err := q.FinishRescanRequest(ctx, db.FinishRescanRequestParams{ID: req.ID, Imported: 2}); err != nil {
		t.Fatal(err)
	}
	if pending, _ := q.ListPendingRescanRequests(ctx); len(pending) != 0 {
		t.Fatal("a finished request is still pending")
	}
	rows, err := q.ListRecentRescanRequests(ctx, 20)
	if err != nil || len(rows) != 1 || rows[0].Email.String != "r@x" || rows[0].Imported != 2 {
		t.Fatalf("recent %+v %v", rows, err)
	}
}
