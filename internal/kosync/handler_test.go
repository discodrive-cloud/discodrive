package kosync_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/kosync"
	"discodrive/internal/secret"
)

const (
	testKey      = "test-kosync-key-exactly32bytes!!"
	testPassword = "kosyncpass456"
	testEmail1   = "reader1@k.test"
	testEmail2   = "reader2@k.test"
)

// md5Key computes the md5 hex string of a plaintext password, matching KOReader's auth scheme.
func md5Key(password string) string {
	sum := md5.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}

// setupKosync spins up a Postgres test container, runs migrations, seeds two users with
// ebook_settings enabled, and returns handlers for both users plus the cipher.
func setupKosync(t *testing.T) (h *kosync.Handler, q *db.Queries, user1ID, user2ID pgtype.UUID, cipher *secret.Cipher, ctx context.Context) {
	t.Helper()
	ctx = context.Background()

	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("Docker required: %v", err)
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

	q = db.New(pool)

	tenant, err := q.CreateTenant(ctx, "test")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// User 1.
	u1, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID:     tenant.ID,
		Email:        testEmail1,
		PasswordHash: "irrelevant",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("CreateUser u1: %v", err)
	}
	user1ID = u1.ID

	// User 2.
	u2, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID:     tenant.ID,
		Email:        testEmail2,
		PasswordHash: "irrelevant",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("CreateUser u2: %v", err)
	}
	user2ID = u2.ID

	cipher, err = secret.New(testKey)
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}

	ct, err := cipher.Encrypt(testPassword)
	if err != nil {
		t.Fatalf("cipher.Encrypt: %v", err)
	}

	// Enable ebook settings for user 1.
	_, err = q.UpsertEbookSettings(ctx, db.UpsertEbookSettingsParams{
		UserID:  u1.ID,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpsertEbookSettings u1: %v", err)
	}
	err = q.SetEbookCredentials(ctx, db.SetEbookCredentialsParams{
		UserID:         u1.ID,
		PasswordCipher: pgtype.Text{String: ct, Valid: true},
		ApiKey:         pgtype.Text{String: "apikey-u1", Valid: true},
	})
	if err != nil {
		t.Fatalf("SetEbookCredentials u1: %v", err)
	}

	// Enable ebook settings for user 2 (different password for isolation test).
	ct2, err := cipher.Encrypt("anotherpass789")
	if err != nil {
		t.Fatalf("cipher.Encrypt u2: %v", err)
	}
	_, err = q.UpsertEbookSettings(ctx, db.UpsertEbookSettingsParams{
		UserID:  u2.ID,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpsertEbookSettings u2: %v", err)
	}
	err = q.SetEbookCredentials(ctx, db.SetEbookCredentialsParams{
		UserID:         u2.ID,
		PasswordCipher: pgtype.Text{String: ct2, Valid: true},
		ApiKey:         pgtype.Text{String: "apikey-u2", Valid: true},
	})
	if err != nil {
		t.Fatalf("SetEbookCredentials u2: %v", err)
	}

	h = kosync.New(q, cipher)
	return h, q, user1ID, user2ID, cipher, ctx
}

// doRequest sends a request to the handler with optional x-auth-user/x-auth-key headers.
func doRequest(h *kosync.Handler, method, target, email, key string, body []byte) *httptest.ResponseRecorder {
	var reqBody *bytes.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reqBody)
	if email != "" {
		req.Header.Set("x-auth-user", email)
	}
	if key != "" {
		req.Header.Set("x-auth-key", key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Test 1: GET /users/auth correct credentials → 200 {"username": email}
func TestUsersAuthCorrect(t *testing.T) {
	h, _, _, _, _, _ := setupKosync(t)

	rec := doRequest(h, http.MethodGet, "/users/auth", testEmail1, md5Key(testPassword), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["username"] != testEmail1 {
		t.Errorf("username=%q, want %q", resp["username"], testEmail1)
	}
}

// Test 2: GET /users/auth wrong key → 401
func TestUsersAuthWrongKey(t *testing.T) {
	h, _, _, _, _, _ := setupKosync(t)

	rec := doRequest(h, http.MethodGet, "/users/auth", testEmail1, md5Key("wrongpassword"), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// Test 3: GET /users/auth disabled ebook_settings → 401
func TestUsersAuthDisabledSettings(t *testing.T) {
	h, q, user1ID, _, _, ctx := setupKosync(t)

	_, err := q.UpsertEbookSettings(ctx, db.UpsertEbookSettingsParams{
		UserID:  user1ID,
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("UpsertEbookSettings(disabled): %v", err)
	}

	rec := doRequest(h, http.MethodGet, "/users/auth", testEmail1, md5Key(testPassword), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// Test 4: GET /users/auth unknown user email → 401
func TestUsersAuthUnknownEmail(t *testing.T) {
	h, _, _, _, _, _ := setupKosync(t)

	rec := doRequest(h, http.MethodGet, "/users/auth", "nobody@k.test", md5Key(testPassword), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// Test 5: GET /users/auth missing x-auth-user header → 401
func TestUsersAuthMissingHeader(t *testing.T) {
	h, _, _, _, _, _ := setupKosync(t)

	rec := doRequest(h, http.MethodGet, "/users/auth", "", md5Key(testPassword), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// Test 6: PUT /syncs/progress (authed, valid body) → 200 {"document": ..., "timestamp": ...}
func TestSyncsPut(t *testing.T) {
	h, _, _, _, _, _ := setupKosync(t)

	body, _ := json.Marshal(map[string]any{
		"document":   "mybook.epub",
		"progress":   "KEPUB_PROGRESS:0.45",
		"percentage": 45.0,
		"device":     "KOReader",
		"device_id":  "device-abc",
	})

	rec := doRequest(h, http.MethodPut, "/syncs/progress", testEmail1, md5Key(testPassword), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["document"] != "mybook.epub" {
		t.Errorf("document=%v, want mybook.epub", resp["document"])
	}
	if _, ok := resp["timestamp"]; !ok {
		t.Error("missing timestamp in response")
	}
}

// Test 7: GET /syncs/progress/{document} after PUT → 200 with stored progress data
func TestSyncsGetAfterPut(t *testing.T) {
	h, _, _, _, _, _ := setupKosync(t)

	body, _ := json.Marshal(map[string]any{
		"document":   "testbook.epub",
		"progress":   "KEPUB_PROGRESS:0.72",
		"percentage": 72.0,
		"device":     "KOReader",
		"device_id":  "dev-xyz",
	})
	putRec := doRequest(h, http.MethodPut, "/syncs/progress", testEmail1, md5Key(testPassword), body)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT failed: %d — %s", putRec.Code, putRec.Body.String())
	}

	getRec := doRequest(h, http.MethodGet, "/syncs/progress/testbook.epub", testEmail1, md5Key(testPassword), nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", getRec.Code, getRec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["document"] != "testbook.epub" {
		t.Errorf("document=%v, want testbook.epub", resp["document"])
	}
	if resp["progress"] != "KEPUB_PROGRESS:0.72" {
		t.Errorf("progress=%v, want KEPUB_PROGRESS:0.72", resp["progress"])
	}
	if resp["device_id"] != "dev-xyz" {
		t.Errorf("device_id=%v, want dev-xyz", resp["device_id"])
	}
	// Percentage: float tolerance (JSON number)
	pct, ok := resp["percentage"].(float64)
	if !ok {
		t.Fatalf("percentage not a float: %T", resp["percentage"])
	}
	if pct < 71.9 || pct > 72.1 {
		t.Errorf("percentage=%.2f, want ~72.0", pct)
	}
}

// Test 8: GET /syncs/progress/{unknown-document} → 200 empty {} (NOT 404)
func TestSyncsGetUnknown(t *testing.T) {
	h, _, _, _, _, _ := setupKosync(t)

	rec := doRequest(h, http.MethodGet, "/syncs/progress/no-such-book.epub", testEmail1, md5Key(testPassword), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "{}" {
		t.Errorf("expected empty JSON object {}, got %q", body)
	}
}

// Test 9: Second PUT same document → GET reflects updated progress (upsert)
func TestSyncsPutUpsert(t *testing.T) {
	h, _, _, _, _, _ := setupKosync(t)

	first, _ := json.Marshal(map[string]any{
		"document":   "upsertbook.epub",
		"progress":   "KEPUB_PROGRESS:0.20",
		"percentage": 20.0,
		"device":     "KOReader",
		"device_id":  "dev-1",
	})
	doRequest(h, http.MethodPut, "/syncs/progress", testEmail1, md5Key(testPassword), first)

	second, _ := json.Marshal(map[string]any{
		"document":   "upsertbook.epub",
		"progress":   "KEPUB_PROGRESS:0.80",
		"percentage": 80.0,
		"device":     "KOReader2",
		"device_id":  "dev-2",
	})
	doRequest(h, http.MethodPut, "/syncs/progress", testEmail1, md5Key(testPassword), second)

	getRec := doRequest(h, http.MethodGet, "/syncs/progress/upsertbook.epub", testEmail1, md5Key(testPassword), nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["progress"] != "KEPUB_PROGRESS:0.80" {
		t.Errorf("progress=%v, want KEPUB_PROGRESS:0.80 (upsert should update)", resp["progress"])
	}
	if resp["device_id"] != "dev-2" {
		t.Errorf("device_id=%v, want dev-2", resp["device_id"])
	}
}

// Test 10: User2 GETting user1's document → 200 empty {} (per-user isolation)
func TestSyncsGetIsolation(t *testing.T) {
	h, _, _, _, _, _ := setupKosync(t)

	// User1 PUTs a document.
	body, _ := json.Marshal(map[string]any{
		"document":   "privateBook.epub",
		"progress":   "KEPUB_PROGRESS:0.55",
		"percentage": 55.0,
		"device":     "KOReader",
		"device_id":  "u1-device",
	})
	putRec := doRequest(h, http.MethodPut, "/syncs/progress", testEmail1, md5Key(testPassword), body)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT by user1 failed: %d", putRec.Code)
	}

	// User2 tries to GET user1's document using user2's own valid credentials.
	user2key := md5Key("anotherpass789")
	getRec := doRequest(h, http.MethodGet, "/syncs/progress/privateBook.epub", testEmail2, user2key, nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", getRec.Code, getRec.Body.String())
	}
	body2 := strings.TrimSpace(getRec.Body.String())
	if body2 != "{}" {
		t.Errorf("user2 should see empty {}, got %q (isolation broken)", body2)
	}
}

func TestCredentialsRevokedOnPasswordChange(t *testing.T) {
	h, q, uid, _, _, ctx := setupKosync(t)
	probe := func() int {
		return doRequest(h, http.MethodGet, "/users/auth", testEmail1, md5Key(testPassword), nil).Code
	}
	if code := probe(); code != 200 {
		t.Fatalf("before change: %d", code)
	}
	u, err := q.GetUserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpdatePassword(ctx, db.UpdatePasswordParams{ID: uid, PasswordHash: "changed", PreviousPasswordHash: u.PasswordHash}); err != nil {
		t.Fatal(err)
	}
	if code := probe(); code != 401 {
		t.Fatalf("old ebook credential accepted: %d", code)
	}
}
