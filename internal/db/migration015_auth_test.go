package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

func TestMigration015AuthMFA(t *testing.T) {
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("need Docker: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })

	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)

	tenant, err := q.CreateTenant(ctx, "t")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	u, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID: tenant.ID, Email: "a@test.local", PasswordHash: "x", Role: "user",
		MustChangePassword: false,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.MustChangePassword {
		t.Fatalf("must_change_password should default to false")
	}

	// No second factors yet → both false.
	f, err := q.AvailableMFAFactors(ctx, u.ID)
	if err != nil {
		t.Fatalf("AvailableMFAFactors: %v", err)
	}
	if f.HasTotp || f.HasWebauthn {
		t.Fatalf("expected no MFA factors, got %+v", f)
	}

	// admin-created user is flagged for forced change.
	adminUser, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID: tenant.ID, Email: "b@test.local", PasswordHash: "x", Role: "user",
		MustChangePassword: true,
	})
	if err != nil {
		t.Fatalf("CreateUser admin-provisioned: %v", err)
	}
	if !adminUser.MustChangePassword {
		t.Fatalf("expected must_change_password=true")
	}

	// Changing the password clears the forced-change flag (A.2).
	changed, err := q.UpdatePassword(ctx, db.UpdatePasswordParams{ID: adminUser.ID, PasswordHash: "y", PreviousPasswordHash: adminUser.PasswordHash})
	if err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	if changed.MustChangePassword {
		t.Fatalf("must_change_password should be cleared after a password change")
	}
}
