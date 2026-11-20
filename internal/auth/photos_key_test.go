package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

func p256Key(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return k, spki
}

func signP256(t *testing.T, k *ecdsa.PrivateKey, msg []byte) []byte {
	t.Helper()
	h := sha256.Sum256(msg)
	sig, err := ecdsa.SignASN1(rand.Reader, k, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestVerifyP256(t *testing.T) {
	k, spki := p256Key(t)
	_, other := p256Key(t)
	msg := []byte("challenge bytes")
	sig := signP256(t, k, msg)
	if !verifyP256(spki, msg, sig) {
		t.Fatal("valid signature rejected")
	}
	k384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	spki384, _ := x509.MarshalPKIXPublicKey(&k384.PublicKey)
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	spkiRSA, _ := x509.MarshalPKIXPublicKey(&rk.PublicKey)
	for name, c := range map[string]struct{ spki, msg, sig []byte }{
		"other message": {spki, []byte("other"), sig},
		"other key":     {other, msg, sig},
		"garbage sig":   {spki, msg, []byte{1, 2, 3}},
		"garbage key":   {[]byte{1, 2, 3}, msg, sig},
		"p384 key":      {spki384, msg, sig},
		"rsa key":       {spkiRSA, msg, sig},
	} {
		if verifyP256(c.spki, c.msg, c.sig) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidPhotosKeySPKI(t *testing.T) {
	_, spki := p256Key(t)
	if !validPhotosKeySPKI(spki) {
		t.Fatal("P-256 key refused")
	}
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	spkiRSA, _ := x509.MarshalPKIXPublicKey(&rk.PublicKey)
	if validPhotosKeySPKI(spkiRSA) || validPhotosKeySPKI([]byte("x")) {
		t.Fatal("non-P-256 key accepted")
	}
}

func TestPhotosKeyChallengeToken(t *testing.T) {
	const uid = "11111111-1111-1111-1111-111111111111"
	const kid = "22222222-2222-2222-2222-222222222222"
	iss := NewTokenIssuer("secret", time.Hour)
	ch := []byte("0123456789abcdef0123456789abcdef")
	tok, err := issuePhotosKeyChallenge(iss, uid, kid, 4, ch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := parsePhotosKeyChallenge(iss, tok, uid, kid, ch)
	if err != nil {
		t.Fatalf("valid challenge: %v", err)
	}
	if c.Ver != 4 || c.ID == "" || c.ExpiresAt == nil || time.Until(c.ExpiresAt.Time) > photosKeyChallengeTTL {
		t.Fatalf("claims: %+v", c)
	}
	if _, err := parsePhotosKeyChallenge(iss, tok, "33333333-3333-3333-3333-333333333333", kid, ch); err == nil {
		t.Error("other user accepted")
	}
	if _, err := parsePhotosKeyChallenge(iss, tok, uid, "44444444-4444-4444-4444-444444444444", ch); err == nil {
		t.Error("other key accepted")
	}
	if _, err := parsePhotosKeyChallenge(iss, tok, uid, kid, []byte("another challenge")); err == nil {
		t.Error("other challenge accepted")
	}
	other, _ := iss.IssueMFA(uid, "", 4)
	if _, err := parsePhotosKeyChallenge(iss, other, uid, kid, ch); err == nil {
		t.Error("wrong purpose accepted")
	}
}

// newKeyUser: a service with a DB and a user "k@test.local" / "pw" without 2FA.
func newKeyUser(t *testing.T) (*Service, *db.Queries, string) {
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
	svc := NewService(pool, NewTokenIssuer("secret", time.Hour), nil)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	hash, _ := HashPassword("pw")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "k@test.local", PasswordHash: hash, Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, q, db.UUIDString(u.ID)
}

func TestPhotosKeyFlow(t *testing.T) {
	svc, q, uid := newKeyUser(t)
	ctx := context.Background()
	k, spki := p256Key(t)
	approval, err := svc.ApprovePasskeyWithPassword(ctx, uid, "pw", "", "photos:hidden")
	if err != nil {
		t.Fatal(err)
	}
	kid, err := svc.RegisterPhotosKey(ctx, uid, approval, spki, "iPhone")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := svc.RegisterPhotosKey(ctx, uid, approval, spki, "again"); !errors.Is(err, ErrApproval) {
		t.Errorf("approval reused: %v", err)
	}
	_, rsaSPKI := func() (any, []byte) {
		rk, _ := rsa.GenerateKey(rand.Reader, 2048)
		b, _ := x509.MarshalPKIXPublicKey(&rk.PublicKey)
		return nil, b
	}()
	a2, _ := svc.ApprovePasskeyWithPassword(ctx, uid, "pw", "", "photos:hidden")
	if _, err := svc.RegisterPhotosKey(ctx, uid, a2, rsaSPKI, "bad"); err == nil {
		t.Error("RSA key registered")
	}

	unlock := func() (string, error) {
		ch, ct, err := svc.PhotosKeyChallenge(ctx, uid, kid)
		if err != nil {
			return "", err
		}
		tok, _, err := svc.UnlockWithPhotosKey(ctx, uid, kid, ct, ch, signP256(t, k, ch))
		return tok, err
	}
	tok, err := unlock()
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if err := svc.ValidatePhotosHidden(ctx, uid, tok); err != nil {
		t.Fatalf("issued token invalid: %v", err)
	}

	// a challenge is single-use
	ch, ct, _ := svc.PhotosKeyChallenge(ctx, uid, kid)
	sig := signP256(t, k, ch)
	if _, _, err := svc.UnlockWithPhotosKey(ctx, uid, kid, ct, ch, sig); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, _, err := svc.UnlockWithPhotosKey(ctx, uid, kid, ct, ch, sig); !errors.Is(err, ErrApproval) {
		t.Errorf("replay: %v", err)
	}
	// an expired or used challenge is not a revoked key
	if _, _, err := svc.UnlockWithPhotosKey(ctx, uid, kid, ct, ch, sig); errors.Is(err, ErrKeyRevoked) {
		t.Errorf("replayed challenge reported as revoked key")
	}
	// a wrong signature
	ch, ct, _ = svc.PhotosKeyChallenge(ctx, uid, kid)
	if _, _, err := svc.UnlockWithPhotosKey(ctx, uid, kid, ct, ch, signP256(t, k, append(ch, 1))); !errors.Is(err, ErrApproval) {
		t.Errorf("bad signature: %v", err)
	}

	// a challenge other than the token's
	ch, ct, _ = svc.PhotosKeyChallenge(ctx, uid, kid)
	forged := []byte("forged challenge, signed by the key")
	if _, _, err := svc.UnlockWithPhotosKey(ctx, uid, kid, ct, forged, signP256(t, k, forged)); !errors.Is(err, ErrApproval) {
		t.Errorf("forged challenge: %v", err)
	}

	// a password change revokes the key
	u, _ := q.GetUserByID(ctx, mustUUID(t, uid))
	newHash, _ := HashPassword("pw2")
	if _, err := q.UpdatePassword(ctx, db.UpdatePasswordParams{ID: u.ID, PasswordHash: newHash, PreviousPasswordHash: u.PasswordHash}); err != nil {
		t.Fatal(err)
	}
	// revoked for good, told apart from a stale challenge
	if _, err := unlock(); !errors.Is(err, ErrKeyRevoked) {
		t.Errorf("after password change: %v", err)
	}

	// another user's key id is unknown; a deleted key too
	if _, _, err := svc.PhotosKeyChallenge(ctx, "33333333-3333-3333-3333-333333333333", kid); !errors.Is(err, ErrNoKey) {
		t.Errorf("other user: %v", err)
	}
	if err := svc.DeletePhotosKey(ctx, uid, kid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PhotosKeyChallenge(ctx, uid, kid); !errors.Is(err, ErrNoKey) {
		t.Errorf("deleted key: %v", err)
	}
}

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTrimKeyName(t *testing.T) {
	long := strings.Repeat("я", 80) // 160 bytes
	got := trimKeyName(long)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) > 100 || len(got) > 200 {
		t.Fatalf("trimmed badly: %d runes, valid=%v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if trimKeyName("iPhone") != "iPhone" {
		t.Fatal("short name changed")
	}
	if n := utf8.RuneCountInString(trimKeyName(strings.Repeat("ж", 150))); n != 100 {
		t.Fatalf("want 100 runes, got %d", n)
	}
}
