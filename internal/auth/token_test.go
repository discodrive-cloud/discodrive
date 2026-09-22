package auth

import (
	"testing"
	"time"
)

func TestIssueMFA_CarriesPurpose(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)

	mfa, err := iss.IssueMFA("11111111-1111-1111-1111-111111111111", "t1", 0)
	if err != nil {
		t.Fatalf("IssueMFA: %v", err)
	}
	claims, err := iss.Parse(mfa)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.Pur != "mfa" {
		t.Fatalf("Pur=%q, want \"mfa\"", claims.Pur)
	}
	if claims.Subject != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("Subject=%q", claims.Subject)
	}
}

func TestIssue_FullSessionHasNoPurpose(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	tok, err := iss.Issue("11111111-1111-1111-1111-111111111111", "t1", "user", 0, "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := iss.Parse(tok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.Pur != "" {
		t.Fatalf("full session Pur=%q, want empty", claims.Pur)
	}
}

// A user who asked never to be signed out gets a token with no exp claim at all —
// an "expiry" far in the future would still log them out one day.
func TestIssueTTL_ZeroMeansNoExpiry(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	tok, err := iss.IssueTTL("11111111-1111-1111-1111-111111111111", "t1", "user", 0, "", 0)
	if err != nil {
		t.Fatalf("IssueTTL: %v", err)
	}
	claims, err := iss.Parse(tok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.ExpiresAt != nil {
		t.Fatalf("ExpiresAt=%v, want nil", claims.ExpiresAt)
	}
	if claims.IssuedAt == nil {
		t.Fatal("IssuedAt is nil: the client compares iat to reject stale renewals")
	}
}

// The user's own lifetime wins over the issuer default, in both directions.
func TestIssueTTL_UsesGivenLifetime(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	for _, ttl := range []time.Duration{10 * time.Minute, 30 * 24 * time.Hour} {
		tok, err := iss.IssueTTL("11111111-1111-1111-1111-111111111111", "t1", "user", 0, "", ttl)
		if err != nil {
			t.Fatalf("IssueTTL(%v): %v", ttl, err)
		}
		claims, err := iss.Parse(tok)
		if err != nil {
			t.Fatalf("Parse(%v): %v", ttl, err)
		}
		got := claims.ExpiresAt.Sub(claims.IssuedAt.Time)
		if got != ttl {
			t.Fatalf("lifetime = %v, want %v", got, ttl)
		}
	}
}

func TestSessionTTL(t *testing.T) {
	cases := map[int32]time.Duration{0: 0, -5: 0, 60: time.Hour, 43200: 30 * 24 * time.Hour}
	for minutes, want := range cases {
		if got := SessionTTL(minutes); got != want {
			t.Errorf("SessionTTL(%d) = %v, want %v", minutes, got, want)
		}
	}
}
