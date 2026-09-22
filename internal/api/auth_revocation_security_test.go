package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/secret"
	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp/totp"
)

// Regression tests exercise real credentials and request handlers against isolated PostgreSQL.
func TestSecurityMFAAfterPasswordChange(t *testing.T) {
	ctx := context.Background()
	pool, _, _ := bootstrapPairingDB(t)
	cipher, err := secret.New(strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(pool, auth.NewTokenIssuer("secret", time.Hour), cipher)
	_, u, err := svc.Register(ctx, "mfa-review@example.test", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	uid := db.UUIDString(u.ID)
	_, key, err := svc.SetupTOTP(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ConfirmTOTP(ctx, uid, code); err != nil {
		t.Fatal(err)
	}
	login, err := svc.Login(ctx, u.Email, "old-password")
	if err != nil || login.MFAToken == "" {
		t.Fatalf("login: %v", err)
	}
	if _, err = svc.ChangePassword(ctx, uid, "old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	code, err = totp.GenerateCode(key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMFATOTP(ctx, login.MFAToken, code); !errors.Is(err, auth.ErrInvalidMFAToken) {
		t.Fatalf("old MFA challenge accepted after password change: %v", err)
	}
	fresh, err := svc.Login(ctx, u.Email, "new-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMFATOTP(ctx, fresh.MFAToken, "wrong"); !errors.Is(err, auth.ErrInvalidTOTPCode) {
		t.Fatalf("invalid MFA code: %v", err)
	}
	if result, err := svc.CompleteMFATOTP(ctx, fresh.MFAToken, code); err != nil || result.Token == "" {
		t.Fatalf("fresh MFA failed: %v", err)
	}
	alternate := alternateJWTSignature(t, fresh.MFAToken)
	if _, err := issuerForSecurityTest().Parse(alternate); err != nil {
		t.Fatalf("alternate signature not valid: %v", err)
	}
	if _, err := svc.CompleteMFATOTP(ctx, alternate, code); !errors.Is(err, auth.ErrInvalidMFAToken) {
		t.Fatalf("MFA replay via alternate encoding: %v", err)
	}
	if _, err := svc.CompleteMFATOTP(ctx, fresh.MFAToken, code); !errors.Is(err, auth.ErrInvalidMFAToken) {
		t.Fatalf("MFA replay accepted: %v", err)
	}

	pending, err := svc.Login(ctx, u.Email, "new-password")
	if err != nil {
		t.Fatal(err)
	}
	outcomes := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() { <-start; _, err := svc.CompleteMFATOTP(ctx, pending.MFAToken, code); outcomes <- err }()
	}
	close(start)
	successes := 0
	for i := 0; i < 8; i++ {
		err := <-outcomes
		if err == nil {
			successes++
		} else if !errors.Is(err, auth.ErrInvalidMFAToken) {
			t.Fatalf("concurrent MFA: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("MFA successes=%d, want 1", successes)
	}

}

func TestSecurityStreamAfterDeviceRevocation(t *testing.T) {
	e := buildStreamEnv(t, "device-stream-review@example.test")
	dev, err := e.q.CreateDesktopDevice(e.ctx, db.CreateDesktopDeviceParams{UserID: e.user.ID, Name: "review-device"})
	if err != nil {
		t.Fatal(err)
	}
	secretToken, err := auth.NewDeviceToken()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.q.SetDeviceTokenHash(e.ctx, db.SetDeviceTokenHashParams{ID: dev.ID, TokenHash: pgtype.Text{String: auth.TokenHash(secretToken), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	bearer, err := e.svc.DeviceTokenExchange(e.ctx, secretToken)
	if err != nil {
		t.Fatal(err)
	}
	rec := e.mediaReq(t, bearer, db.UUIDString(e.folder.ID), "")
	if rec.Code != 200 {
		t.Fatalf("listing: %d %s", rec.Code, rec.Body.String())
	}
	var listing struct {
		Items []struct {
			StreamURL string `json:"stream_url"`
		}
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &listing); err != nil || len(listing.Items) != 1 {
		t.Fatalf("listing parse: %v", err)
	}
	if err = e.q.DeleteDevice(e.ctx, db.DeleteDeviceParams{ID: dev.ID, UserID: e.user.ID}); err != nil {
		t.Fatal(err)
	}
	if got := e.mediaReq(t, bearer, db.UUIDString(e.folder.ID), ""); got.Code != 401 {
		t.Fatalf("device bearer not revoked: %d", got.Code)
	}
	r := httptest.NewRequest("GET", listing.Items[0].StreamURL, nil)
	r.SetPathValue("id", db.UUIDString(e.track.ID))
	result := httptest.NewRecorder()
	e.s.handleStream(result, r)
	if result.Code != http.StatusUnauthorized {
		t.Fatalf("stream: %d %s", result.Code, result.Body.String())
	}

}

func TestSecurityWebAuthnAssertionReplay(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	_, u, err := svc.Register(ctx, "passkey-review@example.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	wa, err := auth.NewWebAuthn("disco.example.com")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetWebAuthn(wa)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	cred := webauthn.Credential{ID: []byte("review-credential"), PublicKey: public}
	blob, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.InsertWebAuthnCredential(ctx, db.InsertWebAuthnCredentialParams{UserID: u.ID, CredentialID: cred.ID, Credential: blob, Name: "review"}); err != nil {
		t.Fatal(err)
	}
	approvalMode := false
	verificationFlags := byte(5)
	ceremony := func() (string, []byte) {
		t.Helper()
		var sessionToken string
		var err error
		if approvalMode {
			_, sessionToken, err = svc.BeginPasskeyApproval(ctx, db.UUIDString(u.ID), "register")
		} else {
			_, sessionToken, err = svc.BeginWebAuthnLogin(ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
		issuer := auth.NewTokenIssuer("secret", time.Hour)
		_, data, err := issuer.ParseWebAuthnSession(sessionToken)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			t.Fatal(err)
		}
		var sd webauthn.SessionData
		if err = json.Unmarshal(decoded, &sd); err != nil {
			t.Fatal(err)
		}
		client, err := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": sd.Challenge, "origin": "https://disco.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		rp := sha256.Sum256([]byte("disco.example.com"))
		ad := append(rp[:], verificationFlags, 0, 0, 0, 0)
		binary.BigEndian.PutUint32(ad[33:], 0)
		clientHash := sha256.Sum256(client)
		signed := append(append([]byte{}, ad...), clientHash[:]...)
		digest := sha256.Sum256(signed)
		signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		enc := base64.RawURLEncoding.EncodeToString
		assertion, err := json.Marshal(map[string]any{"id": enc(cred.ID), "rawId": enc(cred.ID), "type": "public-key", "response": map[string]any{"authenticatorData": enc(ad), "clientDataJSON": enc(client), "signature": enc(signature), "userHandle": enc(u.ID.Bytes[:])}, "clientExtensionResults": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		return sessionToken, assertion
	}
	sessionToken, assertion := ceremony()
	if _, err := svc.FinishWebAuthnLogin(ctx, sessionToken, []byte(`{}`)); err == nil {
		t.Fatal("invalid assertion accepted")
	}

	if result, err := svc.FinishWebAuthnLogin(ctx, sessionToken, assertion); err != nil || result.Token == "" {
		t.Fatalf("first assertion: %v", err)
	}
	alternate := alternateJWTSignature(t, sessionToken)
	if _, _, err := issuerForSecurityTest().ParseWebAuthnSession(alternate); err != nil {
		t.Fatalf("alternate signature not valid: %v", err)
	}
	if _, err := svc.FinishWebAuthnLogin(ctx, alternate, assertion); !errors.Is(err, auth.ErrWebAuthnSession) {
		t.Fatalf("WebAuthn replay via alternate encoding: %v", err)
	}
	if _, err := svc.FinishWebAuthnLogin(ctx, sessionToken, assertion); !errors.Is(err, auth.ErrWebAuthnSession) {
		t.Fatalf("assertion replay accepted: %v", err)
	}

	// Distinct service instances share the replay ledger. Exactly one concurrent
	// request may succeed, including when authenticators have no monotonic counter.
	second := auth.NewService(pool, issuerForSecurityTest(), nil)
	second.SetWebAuthn(wa)
	sessionToken, assertion = ceremony()
	const contenders = 8
	results := make(chan error, contenders)
	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		instance := svc
		if i%2 == 0 {
			instance = second
		}
		go func() { <-start; _, err := instance.FinishWebAuthnLogin(ctx, sessionToken, assertion); results <- err }()
	}
	close(start)
	successes := 0
	for i := 0; i < contenders; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, auth.ErrWebAuthnSession) {
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent successes: %d, want 1", successes)
	}
	if _, err := second.FinishWebAuthnLogin(ctx, sessionToken, assertion); !errors.Is(err, auth.ErrWebAuthnSession) {
		t.Fatalf("replay on new instance: %v", err)
	}

	// Existing-key confirmation is separate from login and requires user verification.
	approvalMode = true
	stepToken, stepAssertion := ceremony()
	if _, err := svc.FinishWebAuthnLogin(ctx, stepToken, stepAssertion); !errors.Is(err, auth.ErrWebAuthnSession) {
		t.Fatalf("approval challenge accepted as login: %v", err)
	}
	grant, err := svc.FinishPasskeyApproval(ctx, db.UUIDString(u.ID), stepToken, stepAssertion)
	if err != nil {
		t.Fatalf("existing-key confirmation: %v", err)
	}
	if _, _, err := svc.BeginWebAuthnRegistration(ctx, db.UUIDString(u.ID), grant); err != nil {
		t.Fatalf("passkey grant: %v", err)
	}
	if _, err := svc.FinishPasskeyApproval(ctx, db.UUIDString(u.ID), stepToken, stepAssertion); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("approval assertion replay: %v", err)
	}
	verificationFlags = 1
	stepToken, stepAssertion = ceremony()
	if _, err := svc.FinishPasskeyApproval(ctx, db.UUIDString(u.ID), stepToken, stepAssertion); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("confirmation without UV accepted: %v", err)
	}
	approvalMode = false
	verificationFlags = 5
	stepToken, stepAssertion = ceremony()
	if _, err := svc.FinishPasskeyApproval(ctx, db.UUIDString(u.ID), stepToken, stepAssertion); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("ordinary login substituted for approval: %v", err)
	}

}

func TestSecurityRefreshTokenAfterPasswordChange(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	_, u, err := svc.Register(ctx, "refresh-review@example.test", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := q.CreateDesktopDevice(ctx, db.CreateDesktopDeviceParams{UserID: u.ID, Name: "review-device"})
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := auth.NewDeviceToken()
	if err != nil {
		t.Fatal(err)
	}
	if err = q.SetDeviceTokenHash(ctx, db.SetDeviceTokenHashParams{ID: dev.ID, TokenHash: pgtype.Text{String: auth.TokenHash(refresh), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	oldAccess, err := svc.DeviceTokenExchange(ctx, refresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ChangePassword(ctx, db.UUIDString(u.ID), "old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	request := func(token string) int {
		r := httptest.NewRequest("GET", "/me", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
		return w.Code
	}
	if code := request(oldAccess); code != 401 {
		t.Fatalf("old access not revoked: %d", code)
	}
	if _, err := svc.DeviceTokenExchange(ctx, refresh); !errors.Is(err, auth.ErrDeviceToken) {
		t.Fatalf("old refresh accepted after password change: %v", err)
	}
	lateToken, err := auth.NewDeviceToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetDeviceTokenHash(ctx, db.SetDeviceTokenHashParams{ID: dev.ID, TokenHash: pgtype.Text{String: auth.TokenHash(lateToken), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeviceTokenExchange(ctx, lateToken); !errors.Is(err, auth.ErrDeviceToken) {
		t.Fatalf("late pairing revived old device: %v", err)
	}
	// A newly approved device carries the current generation and can sign in.
	current, err := q.GetUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	freshDev, err := q.CreateDesktopDevice(ctx, db.CreateDesktopDeviceParams{UserID: u.ID, Name: "fresh", TokenVersion: current.TokenVersion})
	if err != nil {
		t.Fatal(err)
	}
	freshToken, err := auth.NewDeviceToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetDeviceTokenHash(ctx, db.SetDeviceTokenHashParams{ID: freshDev.ID, TokenHash: pgtype.Text{String: auth.TokenHash(freshToken), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	freshAccess, err := svc.DeviceTokenExchange(ctx, freshToken)
	if err != nil || request(freshAccess) != 204 {
		t.Fatalf("fresh device rejected: %v", err)
	}

}

func issuerForSecurityTest() *auth.TokenIssuer { return auth.NewTokenIssuer("secret", time.Hour) }

func TestSecurityDAVPasswordChangeAndInFlightIssuance(t *testing.T) {
	ctx := context.Background()
	_, _, svc := bootstrapPairingDB(t)
	oldToken, user, err := svc.Register(ctx, "dav-revoke@example.test", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	uid := db.UUIDString(user.ID)
	var staleContext context.Context
	var password string
	capture := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		staleContext = r.Context()
		_, password, err = svc.CreateWebdavPassword(r.Context(), uid, "first")
		if err != nil {
			t.Fatal(err)
		}
	}))
	r := httptest.NewRequest("GET", "/me", nil)
	r.Header.Set("Authorization", "Bearer "+oldToken)
	capture.ServeHTTP(httptest.NewRecorder(), r)
	if _, _, ok := svc.VerifyWebdavPassword(ctx, user.Email, password); !ok {
		t.Fatal("new DAV password rejected")
	}
	freshToken, err := svc.ChangePassword(ctx, uid, "old-password", "new-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := svc.VerifyWebdavPassword(ctx, user.Email, password); ok {
		t.Fatal("old DAV password survived password change")
	}
	// Resume issuance that was authorized by middleware before password change.
	_, latePassword, err := svc.CreateWebdavPassword(staleContext, uid, "late")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := svc.VerifyWebdavPassword(ctx, user.Email, latePassword); ok {
		t.Fatal("in-flight DAV issuance upgraded old session")
	}
	r.Header.Set("Authorization", "Bearer "+freshToken)
	capture.ServeHTTP(httptest.NewRecorder(), r)
	if _, _, ok := svc.VerifyWebdavPassword(ctx, user.Email, password); !ok {
		t.Fatal("fresh session cannot create working DAV password")
	}
}

func TestSecurityWebAuthnRegistrationOneTimeAndRevocation(t *testing.T) {
	ctx := context.Background()
	_, _, svc := bootstrapPairingDB(t)
	_, user, err := svc.Register(ctx, "wa-registration@example.test", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	uid := db.UUIDString(user.ID)
	wa, err := auth.NewWebAuthn("disco.example.com")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetWebAuthn(wa)
	password := "old-password"
	ceremony := func() (string, []byte) {
		t.Helper()
		approval, err := svc.ApprovePasskeyWithPassword(ctx, uid, password, "", "register")
		if err != nil {
			t.Fatal(err)
		}
		_, token, err := svc.BeginWebAuthnRegistration(ctx, uid, approval)
		if err != nil {
			t.Fatal(err)
		}
		_, data, err := issuerForSecurityTest().ParseWebAuthnSession(token)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			t.Fatal(err)
		}
		var sd webauthn.SessionData
		if err := json.Unmarshal(raw, &sd); err != nil {
			t.Fatal(err)
		}
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		public, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32))})
		if err != nil {
			t.Fatal(err)
		}
		id := make([]byte, 32)
		if _, err := rand.Read(id); err != nil {
			t.Fatal(err)
		}
		rp := sha256.Sum256([]byte("disco.example.com"))
		ad := append(rp[:], 0x45, 0, 0, 0, 0) // UP, UV, attested credential data; counter=0
		ad = append(ad, make([]byte, 16)...)
		ad = append(ad, 0, byte(len(id)))
		ad = append(ad, id...)
		ad = append(ad, public...)
		object, err := cbor.Marshal(map[string]any{"fmt": "none", "authData": ad, "attStmt": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		client, err := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": sd.Challenge, "origin": "https://disco.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		enc := base64.RawURLEncoding.EncodeToString
		response, err := json.Marshal(map[string]any{"id": enc(id), "rawId": enc(id), "type": "public-key", "response": map[string]any{"attestationObject": enc(object), "clientDataJSON": enc(client)}, "clientExtensionResults": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		return token, response
	}
	token, response := ceremony()
	// A pre-upgrade registration ceremony has no approval. Even a valid attestation
	// must not let a caller bypass the new begin endpoint by finishing one directly.
	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return []byte("secret"), nil })
	if err != nil {
		t.Fatal(err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	delete(claims, "approval")
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.FinishWebAuthnRegistration(ctx, uid, legacy, response, "legacy"); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("finish bypass without approval: %v", err)
	}

	if err := svc.FinishWebAuthnRegistration(ctx, uid, token, response, "test"); err != nil {
		t.Fatalf("registration: %v", err)
	}
	if err := svc.FinishWebAuthnRegistration(ctx, uid, token, response, "replay"); !errors.Is(err, auth.ErrApproval) {
		t.Fatalf("registration replay: %v", err)
	}
	token, response = ceremony()
	if _, err := svc.ChangePassword(ctx, uid, "old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	if err := svc.FinishWebAuthnRegistration(ctx, uid, token, response, "stale"); !errors.Is(err, auth.ErrWebAuthnSession) {
		t.Fatalf("stale registration: %v", err)
	}
	password = "new-password"
	token, response = ceremony()
	if err := svc.FinishWebAuthnRegistration(ctx, uid, token, response, "new"); err != nil {
		t.Fatalf("fresh registration: %v", err)
	}
}

// HS256 signatures end with two unused base64 bits. Changing only those bits
// preserves the signature bytes; replay protection must use the signed ID.
func alternateJWTSignature(t *testing.T, token string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	i := strings.IndexByte(alphabet, token[len(token)-1])
	if i < 0 || i%4 != 0 {
		t.Fatal("unexpected noncanonical HS256 token")
	}
	return token[:len(token)-1] + string(alphabet[i+1])
}

func TestSecurityLegacyChallengesAndStreamRejected(t *testing.T) {
	e := buildStreamEnv(t, "legacy-revoke@example.test")
	for _, purpose := range []string{"mfa", "stream"} {
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{
			Pur: purpose, Ver: e.user.TokenVersion, Nid: db.UUIDString(e.track.ID),
			RegisteredClaims: jwt.RegisteredClaims{Subject: e.userID, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))},
		}).SignedString([]byte("secret"))
		if err != nil {
			t.Fatal(err)
		}
		if purpose == "mfa" {
			if _, err := e.svc.CompleteMFATOTP(e.ctx, token, "123456"); !errors.Is(err, auth.ErrInvalidMFAToken) {
				t.Fatalf("legacy MFA: %v", err)
			}
		} else {
			if _, err := e.svc.ValidateStreamToken(e.ctx, token, db.UUIDString(e.track.ID)); !errors.Is(err, auth.ErrStreamToken) {
				t.Fatalf("legacy stream: %v", err)
			}
		}
	}
	waLegacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"pur": "webauthn-login", "was": "ZGF0YQ==", "exp": time.Now().Add(time.Minute).Unix()}).SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := issuerForSecurityTest().ParseWebAuthnSession(waLegacy); err == nil {
		t.Fatal("legacy stateless WebAuthn accepted")
	}
}
