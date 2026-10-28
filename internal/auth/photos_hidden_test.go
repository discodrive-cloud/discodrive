package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

func TestValidatePhotosHidden(t *testing.T) {
	const uid = "11111111-1111-1111-1111-111111111111"
	iss := NewTokenIssuer("secret", time.Hour)
	ver := int64(3)
	svc := &Service{issuer: iss, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
		return db.User{TokenVersion: ver}, nil
	}}
	mk := func(pur, sub string, v int64, exp time.Time) string {
		c := Claims{Pur: pur, Ver: v, RegisteredClaims: jwt.RegisteredClaims{Subject: sub, ExpiresAt: jwt.NewNumericDate(exp)}}
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(iss.secret)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	ctx := context.Background()
	soon := time.Now().Add(time.Minute)
	if err := svc.ValidatePhotosHidden(ctx, uid, mk("photos-hidden", uid, 3, soon)); err != nil {
		t.Fatalf("valid token: %v", err)
	}
	for name, tok := range map[string]string{
		"expired":       mk("photos-hidden", uid, 3, time.Now().Add(-time.Minute)),
		"wrong purpose": mk("stream-v2", uid, 3, soon),
		"other user":    mk("photos-hidden", "22222222-2222-2222-2222-222222222222", 3, soon),
		"old version":   mk("photos-hidden", uid, 2, soon),
		"garbage":       "x.y.z",
	} {
		if err := svc.ValidatePhotosHidden(ctx, uid, tok); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
