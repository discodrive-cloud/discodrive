package auth

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"discodrive/internal/db"
)

// countArgon2 replaces the verification seam with a counter for the test's duration.
func countArgon2(t *testing.T) *atomic.Int32 {
	t.Helper()
	n := new(atomic.Int32)
	orig := verifyPassword
	verifyPassword = func(pw, encoded string) (bool, error) {
		n.Add(1)
		return orig(pw, encoded)
	}
	t.Cleanup(func() { verifyPassword = orig })
	return n
}

// An unknown email used to answer at once, a known one only after Argon2 (~0.5 s on a
// Pi): the response time told which accounts exist. Both must cost one Argon2 run.
func TestLoginUnknownEmailCostsOneArgon2(t *testing.T) {
	n := countArgon2(t)
	svc := buildLoginService(t, "", nil)
	svc.getUserByEmail = func(context.Context, string) (db.User, error) { return db.User{}, pgx.ErrNoRows }
	if _, err := svc.Login(context.Background(), "nobody@test.local", "whatever"); err != ErrInvalidCreds {
		t.Fatalf("err=%v, want ErrInvalidCreds", err)
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("unknown email ran Argon2 %d times, want 1 (same as a wrong password)", got)
	}
}

func TestDummyHashMatchesCurrentParams(t *testing.T) {
	ok, err := VerifyPassword("", dummyHash)
	if err != nil || ok {
		t.Fatalf("dummy hash must parse and match nothing: ok=%v err=%v", ok, err)
	}
	want, _ := HashPassword("x")
	// Same "$argon2id$v=..$m=..,t=..,p=..$" prefix: the dummy costs what a real check costs.
	prefix := func(h string) string {
		i, dollars := 0, 0
		for ; i < len(h) && dollars < 4; i++ {
			if h[i] == '$' {
				dollars++
			}
		}
		return h[:i]
	}
	if prefix(dummyHash) != prefix(want) {
		t.Fatalf("dummy params %q differ from real %q", prefix(dummyHash), prefix(want))
	}
}

// WebDAV Basic: an email with no app passwords (or no account at all) answered without
// Argon2, so timing revealed who has DAV access. The dummy check must run, and under the
// shared admission slot, like a real one.
func TestDAVUnknownCredentialsCostOneArgon2UnderSlot(t *testing.T) {
	f := newDAVFixture(t)
	var outsideSlot atomic.Int32
	inner := verifyPassword
	verifyPassword = func(pw, encoded string) (bool, error) {
		if len(davCheckSlots) == 0 {
			outsideSlot.Add(1)
		}
		return inner(pw, encoded)
	}
	t.Cleanup(func() { verifyPassword = inner })

	ctx := context.Background()
	q := db.New(f.pool)
	tenant, err := q.CreateTenant(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "nodav@test.local", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"nobody@test.local", "nodav@test.local"} {
		before := f.argon2.Load()
		if _, _, ok := f.svc.VerifyWebdavPassword(ctx, email, "some-password"); ok {
			t.Fatalf("%s: accepted", email)
		}
		if got := f.argon2.Load() - before; got != 1 {
			t.Fatalf("%s: Argon2 ran %d times, want 1", email, got)
		}
	}
	if outsideSlot.Load() != 0 {
		t.Fatal("the dummy Argon2 check ran without holding a DAV admission slot")
	}
}
