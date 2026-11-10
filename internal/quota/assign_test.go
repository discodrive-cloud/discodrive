package quota_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/quota"
)

const gib = int64(1) << 30

// setup spins up a throwaway Postgres, migrates it, and returns a query set.
func setup(t *testing.T) (*db.Queries, context.Context) {
	t.Helper()
	q, _, ctx := setupPool(t)
	return q, ctx
}

func setupPool(t *testing.T) (*db.Queries, *pgxpool.Pool, context.Context) {
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
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return db.New(pool), pool, ctx
}

// makeUser creates a user with the given quota (nil = none).
func makeUser(t *testing.T, q *db.Queries, ctx context.Context, email string, userQuota *int64) pgtype.UUID {
	t.Helper()
	tenant, err := q.CreateTenant(ctx, email)
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	sq := pgtype.Int8{}
	if userQuota != nil {
		sq = pgtype.Int8{Int64: *userQuota, Valid: true}
	}
	u, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID: tenant.ID, Email: email, PasswordHash: "x", Role: "user", StorageQuota: sq,
	})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	return u.ID
}

// The server-wide cap bounds what the admin can hand out in total: with 1 TiB of disk
// budgeted, two users cannot walk away with 700 GiB each.
func TestCheckAssign_CapBoundsTheSumOfQuotas(t *testing.T) {
	q, ctx := setup(t)
	c := quota.New(q, 1024*gib)

	first := 700 * gib
	makeUser(t, q, ctx, "a@x", &first)

	second := 700 * gib
	err := c.CheckAssign(ctx, pgtype.UUID{}, &second)
	if !errors.Is(err, quota.ErrOvercommit) {
		t.Fatalf("want ErrOvercommit, got %v", err)
	}
	fits := 300 * gib
	if err := c.CheckAssign(ctx, pgtype.UUID{}, &fits); err != nil {
		t.Fatalf("300 GiB must still fit under a 1 TiB cap with 700 GiB assigned: %v", err)
	}
}

// Editing a user replaces their quota, so their current one must not count against
// the new value — otherwise raising a quota from 700 to 800 GiB under a 1 TiB cap
// would look like a request for 1500 GiB.
func TestCheckAssign_ExcludesTheUserBeingEdited(t *testing.T) {
	q, ctx := setup(t)
	c := quota.New(q, 1024*gib)

	current := 700 * gib
	id := makeUser(t, q, ctx, "a@x", &current)

	raised := 800 * gib
	if err := c.CheckAssign(ctx, id, &raised); err != nil {
		t.Fatalf("raising the same user's quota within the cap: %v", err)
	}
	if err := c.CheckAssign(ctx, pgtype.UUID{}, &raised); !errors.Is(err, quota.ErrOvercommit) {
		t.Fatalf("without the exclusion the same number must not fit, got %v", err)
	}
}

// No cap configured means no ceiling on assignment: this stays opt-in.
func TestCheckAssign_NoCapAllowsAnything(t *testing.T) {
	q, ctx := setup(t)
	c := quota.New(q, 0)

	huge := 9000 * gib
	if err := c.CheckAssign(ctx, pgtype.UUID{}, &huge); err != nil {
		t.Fatalf("with no cap any quota is allowed: %v", err)
	}
	if _, capped, err := c.Assignable(ctx, pgtype.UUID{}); err != nil || capped {
		t.Fatalf("Assignable must report no cap: capped=%v err=%v", capped, err)
	}
}

// A user with no personal quota is unlimited, but only up to the server-wide cap.
func TestAllowance_UserWithoutQuota(t *testing.T) {
	q, ctx := setup(t)
	id := makeUser(t, q, ctx, "a@x", nil)

	if got, err := quota.New(q, 0).Allowance(ctx, id); err != nil || got != quota.Unlimited {
		t.Fatalf("no quota and no cap must be unlimited: got %d err %v", got, err)
	}
	got, err := quota.New(q, 500).Allowance(ctx, id)
	if err != nil {
		t.Fatalf("allowance: %v", err)
	}
	if got != 500 {
		t.Fatalf("allowance under a 500-byte cap = %d, want 500", got)
	}
}

// Assignable tells the admin panel how much is left to hand out.
func TestAssignable(t *testing.T) {
	q, ctx := setup(t)
	assigned := 400 * gib
	makeUser(t, q, ctx, "a@x", &assigned)
	makeUser(t, q, ctx, "b@x", nil) // no quota — nothing to subtract

	free, capped, err := quota.New(q, 1024*gib).Assignable(ctx, pgtype.UUID{})
	if err != nil || !capped {
		t.Fatalf("Assignable: capped=%v err=%v", capped, err)
	}
	if want := 624 * gib; free != want {
		t.Fatalf("assignable = %d, want %d", free, want)
	}
}
