package api

import (
	"context"
	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/secret"
	"errors"
	"github.com/pquerna/otp/totp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasskeyApprovalRequiresExistingFactors(t *testing.T) {
	ctx := context.Background()
	pool, q, _ := bootstrapPairingDB(t)
	cipher, err := secret.New(strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(pool, issuerForSecurityTest(), cipher)
	stolen, user, err := svc.Register(ctx, "approval@example.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	uid := db.UUIDString(user.ID)
	wa, err := auth.NewWebAuthn("disco.example.com")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetWebAuthn(wa)
	server := &Server{auth: svc, q: q}
	r := httptest.NewRequest("POST", "/me/webauthn/register/begin", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+stolen)
	w := httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(server.handleWebAuthnRegisterBegin)).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("stolen session begin=%d", w.Code)
	}
	if _, _, err := svc.BeginWebAuthnRegistration(ctx, uid); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("service bypass: %v", err)
	}
	if _, err := svc.ApprovePasskeyWithPassword(ctx, uid, "wrong", "", "register"); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("wrong password: %v", err)
	}
	_, key, err := svc.SetupTOTP(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmTOTP(ctx, uid, code); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApprovePasskeyWithPassword(ctx, uid, "password12", "", "register"); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("MFA omitted: %v", err)
	}
	approval, err := svc.ApprovePasskeyWithPassword(ctx, uid, "password12", code, "register")
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := svc.Register(ctx, "approval-other@example.test", "password-other")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.BeginWebAuthnRegistration(ctx, db.UUIDString(other.ID), approval); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("grant accepted for another user: %v", err)
	}
	if _, _, err := svc.BeginWebAuthnRegistration(ctx, uid, approval); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "/me", nil)
	r.Header.Set("Authorization", "Bearer "+approval)
	w = httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("approval grants session") })).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("approval session status=%d", w.Code)
	}
	credential, err := q.InsertWebAuthnCredential(ctx, db.InsertWebAuthnCredentialParams{UserID: user.ID, CredentialID: []byte("test-delete"), Credential: []byte(`{}`), Name: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	id := db.UUIDString(credential.ID)
	if err := svc.DeletePasskey(ctx, uid, id, approval); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("wrong action: %v", err)
	}
	deletion, err := svc.ApprovePasskeyWithPassword(ctx, uid, "password12", code, "delete:"+id)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeletePasskey(ctx, uid, id, deletion); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeletePasskey(ctx, uid, id, deletion); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("replay: %v", err)
	}
	if _, err := svc.ChangePassword(ctx, uid, "password12", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.BeginWebAuthnRegistration(ctx, uid, approval); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("approval survived password change: %v", err)
	}
}
