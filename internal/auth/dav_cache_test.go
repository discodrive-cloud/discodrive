package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

// davFixture: a user with one WebDAV app password, and a counter of Argon2 checks.
type davFixture struct {
	svc      *Service
	pool     *pgxpool.Pool
	email    string
	password string
	device   pgtype.UUID
	argon2   *atomic.Int32
}

func newDAVFixture(t *testing.T) *davFixture {
	t.Helper()
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
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, err := q.CreateTenant(ctx, "t")
	if err != nil {
		t.Fatal(err)
	}
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "dav@test.local", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	f := &davFixture{svc: NewService(pool, NewTokenIssuer("secret", time.Hour), nil), pool: pool,
		email: "dav@test.local", password: "app-password-1", argon2: new(atomic.Int32)}
	f.device = f.addDevice(t, u, f.password)

	orig := verifyPassword
	verifyPassword = func(pw, encoded string) (bool, error) {
		f.argon2.Add(1)
		return orig(pw, encoded)
	}
	t.Cleanup(func() { verifyPassword = orig })
	return f
}

func (f *davFixture) addDevice(t *testing.T, u db.User, password string) pgtype.UUID {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.New(f.pool).CreateWebdavDevice(context.Background(), db.CreateWebdavDeviceParams{
		UserID: u.ID, Name: "dav", TokenVersion: u.TokenVersion, SecretHash: pgtype.Text{String: hash, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

func (f *davFixture) check(password string) bool {
	_, _, ok := f.svc.VerifyWebdavPassword(context.Background(), f.email, password)
	return ok
}

func (f *davFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// Every WebDAV request used to run Argon2 (64 MiB, ~0.5 s on a Pi): a Finder or rclone
// session hammered the CPU and, on a 256 MB box, OOM-killed the server.
func TestDAVCacheSkipsArgon2ForRepeatedRequests(t *testing.T) {
	f := newDAVFixture(t)
	for range 5 {
		if !f.check(f.password) {
			t.Fatal("valid app password rejected")
		}
	}
	if n := f.argon2.Load(); n != 1 {
		t.Fatalf("Argon2 ran %d times for 5 requests, want 1", n)
	}
}

// The cache must never outlive what makes the password valid: each of these
// invalidates it on the very next request, not after the TTL.
func TestDAVCacheHonoursRevocationImmediately(t *testing.T) {
	cases := map[string]string{
		"device deleted":      `DELETE FROM devices WHERE id = $1`,
		"secret regenerated":  `UPDATE devices SET secret_hash = 'argon2id$other' WHERE id = $1`,
		"sessions revoked":    `UPDATE users SET token_version = token_version + 1 WHERE id = (SELECT user_id FROM devices WHERE id = $1)`,
		"password change due": `UPDATE users SET must_change_password = true WHERE id = (SELECT user_id FROM devices WHERE id = $1)`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDAVFixture(t)
			if !f.check(f.password) {
				t.Fatal("valid app password rejected")
			}
			f.exec(t, sql, f.device)
			if f.check(f.password) {
				t.Fatalf("after %s the cached password was still accepted", name)
			}
		})
	}
}

// Entries live a fixed time from the Argon2 check; use does not extend them.
func TestDAVCacheExpires(t *testing.T) {
	f := newDAVFixture(t)
	now := time.Now()
	f.svc.davCache.now = func() time.Time { return now }
	f.check(f.password)
	now = now.Add(davCacheTTL - time.Second)
	f.check(f.password)
	if n := f.argon2.Load(); n != 1 {
		t.Fatalf("Argon2 ran %d times within the TTL, want 1", n)
	}
	now = now.Add(2 * time.Second)
	if !f.check(f.password) {
		t.Fatal("valid app password rejected after expiry")
	}
	if n := f.argon2.Load(); n != 2 {
		t.Fatalf("Argon2 ran %d times, want a fresh check after the TTL", n)
	}
}

// Only successful checks are cached: a wrong password is verified (and counted by the
// guard) every time.
func TestDAVCacheNeverCachesFailures(t *testing.T) {
	f := newDAVFixture(t)
	for range 3 {
		if f.check("wrong") {
			t.Fatal("wrong password accepted")
		}
	}
	if n := f.argon2.Load(); n != 3 {
		t.Fatalf("Argon2 ran %d times for 3 wrong attempts, want 3", n)
	}
}

func TestDAVCacheIsBounded(t *testing.T) {
	c := newDAVCache()
	for i := range davCacheMax + 100 {
		c.store("u@x", string(rune(i)), davCacheEntry{deviceID: "d"})
	}
	if n := c.len(); n > davCacheMax {
		t.Fatalf("cache holds %d entries, max %d", n, davCacheMax)
	}
}
