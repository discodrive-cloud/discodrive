package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

// A server that already holds files gets its count from them when the migration runs:
// the first quota check after the upgrade sees what the last one before it saw.
func TestMigration041CountsTheFilesAlreadyThere(t *testing.T) {
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("need Docker: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateTo(dsn, 40); err != nil {
		t.Fatalf("migrations up to 40: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)

	tenant, _ := q.CreateTenant(ctx, "t")
	user := func(email string) pgtype.UUID {
		t.Helper()
		u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: email, PasswordHash: "x", Role: "user"})
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	ann, bob, empty := user("ann@m41.test"), user("bob@m41.test"), user("empty@m41.test")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec("INSERT INTO nodes (user_id, name, size) VALUES ($1, 'a', 1000), ($1, 'b', 234), ($1, 'no size', NULL)", ann)
	exec("INSERT INTO nodes (user_id, name, is_dir, size) VALUES ($1, 'dir', true, 4096)", ann)
	exec("INSERT INTO nodes (user_id, name, size, deleted_at) VALUES ($1, 'trashed', 50, now())", ann)
	exec("INSERT INTO file_versions (node_id, version, size) SELECT id, 1, 16 FROM nodes WHERE name = 'a'")
	exec("INSERT INTO nodes (user_id, name, size) VALUES ($1, 'c', 7)", bob)

	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migration 41: %v", err)
	}
	for who, want := range map[pgtype.UUID]int64{ann: 1300, bob: 7, empty: 0} {
		got, err := q.UserStorageUsage(ctx, who)
		if err != nil || got != want {
			t.Errorf("after the migration a user occupies %d bytes (err %v), want %d", got, err, want)
		}
	}
	if drift, err := q.ReconcileFileUsage(ctx); err != nil || len(drift) != 0 {
		t.Errorf("the migrated count is off: %+v (err %v)", drift, err)
	}
	// And the way back leaves a schema the previous server runs on.
	if err := db.MigrateTo(dsn, 40); err != nil {
		t.Fatalf("down to 40: %v", err)
	}
	exec("INSERT INTO nodes (user_id, name, size) VALUES ($1, 'after the rollback', 1)", bob)
}
