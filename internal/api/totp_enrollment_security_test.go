package api

import (
	"context"
	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/secret"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp/totp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTOTPEnrollmentRequiresIdentity(t *testing.T) {
	ctx := context.Background()
	pool, q, _ := bootstrapPairingDB(t)
	cipher, _ := secret.New(strings.Repeat("x", 32))
	svc := auth.NewService(pool, issuerForSecurityTest(), cipher)
	token, u, err := svc.Register(ctx, "totp-enrollment@example.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{auth: svc, q: q}
	r := httptest.NewRequest("POST", "/me/totp/setup", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(server.handleTOTPSetup)).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("stolen session can start TOTP: status=%d", w.Code)
	}
	if _, err := q.GetUserTOTP(ctx, u.ID); err == nil {
		t.Fatal("unauthorized enrollment stored")
	}
}

func TestTOTPConfirmCannotReissueBackupCodes(t *testing.T) {
	ctx := context.Background()
	pool, _, _ := bootstrapPairingDB(t)
	cipher, _ := secret.New(strings.Repeat("x", 32))
	svc := auth.NewService(pool, issuerForSecurityTest(), cipher)
	_, u, err := svc.Register(ctx, "totp-confirm@example.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	uid := db.UUIDString(u.ID)
	enrollmentApproval, err := svc.ApprovePasskeyWithPassword(ctx, uid, "password12", "", "totp:setup")
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := svc.SetupTOTP(ctx, uid, enrollmentApproval)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(key, time.Now())
	if _, err := svc.ConfirmTOTP(ctx, uid, code, enrollmentApproval); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmTOTP(ctx, uid, code, enrollmentApproval); err == nil {
		t.Fatal("repeated confirmation replaced backup codes")
	}
}

func TestTOTPEnrollmentProofBindingAndRaces(t *testing.T) {
	ctx := context.Background()
	pool, q, _ := bootstrapPairingDB(t)
	cipher, _ := secret.New(strings.Repeat("x", 32))
	svc := auth.NewService(pool, issuerForSecurityTest(), cipher)
	otherSvc := auth.NewService(pool, issuerForSecurityTest(), cipher)
	serial := 0
	newUser := func(t *testing.T) string {
		serial++
		_, u, err := svc.Register(ctx, fmt.Sprintf("totp-race-%d@example.test", serial), "password12")
		if err != nil {
			t.Fatal(err)
		}
		return db.UUIDString(u.ID)
	}
	approve := func(t *testing.T, uid, action string) string {
		p, err := svc.ApprovePasskeyWithPassword(ctx, uid, "password12", "", action)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	start := func(t *testing.T, uid, p string) string {
		_, key, err := svc.SetupTOTP(ctx, uid, p)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	t.Run("wrong action user and missing proof", func(t *testing.T) {
		uid := newUser(t)
		other := newUser(t)
		if _, _, err := svc.SetupTOTP(ctx, uid); err == nil {
			t.Fatal("missing proof accepted")
		}
		p := approve(t, uid, "register")
		if _, _, err := svc.SetupTOTP(ctx, uid, p); err == nil {
			t.Fatal("passkey proof accepted")
		}
		p = approve(t, uid, "totp:setup")
		if _, _, err := svc.SetupTOTP(ctx, other, p); err == nil {
			t.Fatal("other user's proof accepted")
		}
		key := start(t, uid, p)
		code, _ := totp.GenerateCode(key, time.Now())
		if _, err := svc.ConfirmTOTP(ctx, uid, code); err == nil {
			t.Fatal("confirmation without proof accepted")
		}
		if _, err := svc.ConfirmTOTP(ctx, uid, "invalid", p); err == nil {
			t.Fatal("invalid code accepted")
		}
		if _, err := svc.ConfirmTOTP(ctx, uid, code, p); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("superseded enrollment cannot confirm or restart", func(t *testing.T) {
		uid := newUser(t)
		p1 := approve(t, uid, "totp:setup")
		p2 := approve(t, uid, "totp:setup")
		key1 := start(t, uid, p1)
		key2 := start(t, uid, p2)
		code1, _ := totp.GenerateCode(key1, time.Now())
		code2, _ := totp.GenerateCode(key2, time.Now())
		if _, err := svc.ConfirmTOTP(ctx, uid, code1, p1); err == nil {
			t.Fatal("superseded enrollment confirmed")
		}
		if _, err := svc.ConfirmTOTP(ctx, uid, code2, p1); err == nil {
			t.Fatal("proof swapped between secrets")
		}
		if _, _, err := svc.SetupTOTP(ctx, uid, p1); err == nil {
			t.Fatal("setup proof reused")
		}
		if _, err := svc.ConfirmTOTP(ctx, uid, code2, p2); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("password change revokes pending enrollment", func(t *testing.T) {
		uid := newUser(t)
		p := approve(t, uid, "totp:setup")
		key := start(t, uid, p)
		if _, err := svc.ChangePassword(ctx, uid, "password12", "new-password12"); err != nil {
			t.Fatal(err)
		}
		code, _ := totp.GenerateCode(key, time.Now())
		if _, err := svc.ConfirmTOTP(ctx, uid, code, p); err == nil {
			t.Fatal("old generation confirmed")
		}
		if _, _, err := svc.SetupTOTP(ctx, uid, p); err == nil {
			t.Fatal("old generation setup accepted")
		}
	})
	t.Run("expired proof rejected at both stages", func(t *testing.T) {
		uid := newUser(t)
		p := approve(t, uid, "totp:setup")
		key := start(t, uid, p)
		claims, err := issuerForSecurityTest().Parse(p)
		if err != nil {
			t.Fatal(err)
		}
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
		expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
		if err != nil {
			t.Fatal(err)
		}
		code, _ := totp.GenerateCode(key, time.Now())
		if _, err := svc.ConfirmTOTP(ctx, uid, code, expired); err == nil {
			t.Fatal("expired confirmation accepted")
		}
		if _, _, err := svc.SetupTOTP(ctx, uid, expired); err == nil {
			t.Fatal("expired setup accepted")
		}
	})
	t.Run("used enrollment cannot return after disable", func(t *testing.T) {
		uid := newUser(t)
		p := approve(t, uid, "totp:setup")
		key := start(t, uid, p)
		code, _ := totp.GenerateCode(key, time.Now())
		if _, err := svc.ConfirmTOTP(ctx, uid, code, p); err != nil {
			t.Fatal(err)
		}
		if err := svc.DisableTOTP(ctx, uid, "password12", code); err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.SetupTOTP(ctx, uid, p); err == nil {
			t.Fatal("used proof recreated disabled factor")
		}
		if _, err := svc.ConfirmTOTP(ctx, uid, code, p); err == nil {
			t.Fatal("old confirmation restored disabled factor")
		}
	})
	t.Run("parallel setup consumes proof once", func(t *testing.T) {
		uid := newUser(t)
		p := approve(t, uid, "totp:setup")
		gate := make(chan struct{})
		results := make(chan error, 2)
		for _, s := range []*auth.Service{svc, otherSvc} {
			go func(s *auth.Service) { <-gate; _, _, err := s.SetupTOTP(ctx, uid, p); results <- err }(s)
		}
		close(gate)
		success := 0
		for i := 0; i < 2; i++ {
			if <-results == nil {
				success++
			}
		}
		if success != 1 {
			t.Fatalf("setup successes=%d", success)
		}
	})
	t.Run("parallel confirmation issues backups once", func(t *testing.T) {
		uid := newUser(t)
		p := approve(t, uid, "totp:setup")
		key := start(t, uid, p)
		code, _ := totp.GenerateCode(key, time.Now())
		gate := make(chan struct{})
		results := make(chan error, 2)
		for _, s := range []*auth.Service{svc, otherSvc} {
			go func(s *auth.Service) { <-gate; _, err := s.ConfirmTOTP(ctx, uid, code, p); results <- err }(s)
		}
		close(gate)
		success := 0
		for i := 0; i < 2; i++ {
			if <-results == nil {
				success++
			}
		}
		if success != 1 {
			t.Fatalf("confirmation successes=%d", success)
		}
	})
	t.Run("setup and confirm serialize without replacing enabled factor", func(t *testing.T) {
		uid := newUser(t)
		p1 := approve(t, uid, "totp:setup")
		p2 := approve(t, uid, "totp:setup")
		key := start(t, uid, p1)
		code, _ := totp.GenerateCode(key, time.Now())
		gate := make(chan struct{})
		setupResult := make(chan error, 1)
		confirmResult := make(chan error, 1)
		go func() { <-gate; _, _, err := otherSvc.SetupTOTP(ctx, uid, p2); setupResult <- err }()
		go func() { <-gate; _, err := svc.ConfirmTOTP(ctx, uid, code, p1); confirmResult <- err }()
		close(gate)
		setupErr, confirmErr := <-setupResult, <-confirmResult
		if (setupErr == nil) == (confirmErr == nil) {
			t.Fatalf("expected one winner: setup=%v confirm=%v", setupErr, confirmErr)
		}
		id, _ := db.ParseUUID(uid)
		row, err := q.GetUserTOTP(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if row.Enabled != (confirmErr == nil) {
			t.Fatal("enabled state does not match confirmed secret")
		}
		if row.Enabled {
			actual, err := cipher.Decrypt(row.Secret)
			if err != nil || actual != key {
				t.Fatal("enabled secret replaced")
			}
		}
	})
}
