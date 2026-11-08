package opds

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/secret"
)

const testKey = "test-opds-key-exactly-32bytes!!!"
const testPassword = "ebookpass123"
const testAPIKey = "opds-apikey-abc"
const testEmail = "reader@x.test"

// setupOPDS spins up a Postgres test container, runs migrations, creates a user
// with ebook_settings enabled, and returns a ready Handler plus the test context.
func setupOPDS(t *testing.T) (*Handler, context.Context) {
	t.Helper()
	ctx := context.Background()

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

	q := db.New(pool)

	// Create tenant + user.
	tenant, err := q.CreateTenant(ctx, "test")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	user, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID:     tenant.ID,
		Email:        testEmail,
		PasswordHash: "irrelevant",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Build cipher and encrypt the test password.
	cipher, err := secret.New(testKey)
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	ct, err := cipher.Encrypt(testPassword)
	if err != nil {
		t.Fatalf("cipher.Encrypt: %v", err)
	}

	// Enable ebook settings (FolderNodeID left zero — not needed for auth tests).
	_, err = q.UpsertEbookSettings(ctx, db.UpsertEbookSettingsParams{
		UserID:  user.ID,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("UpsertEbookSettings: %v", err)
	}

	// Set credentials: encrypted password + plaintext api key.
	err = q.SetEbookCredentials(ctx, db.SetEbookCredentialsParams{
		UserID:         user.ID,
		PasswordCipher: pgtype.Text{String: ct, Valid: true},
		ApiKey:         pgtype.Text{String: testAPIKey, Valid: true},
	})
	if err != nil {
		t.Fatalf("SetEbookCredentials: %v", err)
	}

	h := New(q, cipher, "", false)
	return h, ctx
}

// opdsGet sends a GET to the handler and returns the response recorder.
func opdsGet(h *Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// opdsGetBasic sends a GET with Basic auth credentials.
func opdsGetBasic(h *Handler, target, email, password string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetBasicAuth(email, password)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBasicAuthCorrect(t *testing.T) {
	h, _ := setupOPDS(t)

	rec := opdsGetBasic(h, "/opds", testEmail, testPassword)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

func TestBasicAuthWrongPassword(t *testing.T) {
	h, _ := setupOPDS(t)

	rec := opdsGetBasic(h, "/opds", testEmail, "wrongpassword")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	wwwAuth := rec.Header().Get("WWW-Authenticate")
	if wwwAuth == "" {
		t.Error("missing WWW-Authenticate header on 401")
	}
	want := `Basic realm="discodrive OPDS"`
	if wwwAuth != want {
		t.Errorf("WWW-Authenticate=%q, want %q", wwwAuth, want)
	}
}

func TestAPIKeyValid(t *testing.T) {
	h, _ := setupOPDS(t)

	rec := opdsGet(h, fmt.Sprintf("/opds?apiKey=%s", testAPIKey))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

func TestEnabledFalse(t *testing.T) {
	h, ctx := setupOPDS(t)

	// Disable ebook settings for the test user.
	user, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	_, err = h.q.UpsertEbookSettings(ctx, db.UpsertEbookSettingsParams{
		UserID:  user.ID,
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("UpsertEbookSettings(disabled): %v", err)
	}

	rec := opdsGetBasic(h, "/opds", testEmail, testPassword)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestUnknownEmail(t *testing.T) {
	h, _ := setupOPDS(t)

	rec := opdsGetBasic(h, "/opds", "nobody@x.test", testPassword)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestNoCredentials(t *testing.T) {
	h, _ := setupOPDS(t)
	req := httptest.NewRequest(http.MethodGet, "/opds", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Error("expected WWW-Authenticate header, got none")
	}
}

// The apiKey is a query parameter. Reading it with FormValue made the handler parse a
// multipart body before any credential check, and that parse happens on a request
// clone whose temp files net/http never removes. A key sent only in the body must not
// authenticate: proof that the body is not parsed.
func TestAPIKeyInBodyIsNotRead(t *testing.T) {
	h, _ := setupOPDS(t)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("apiKey", testAPIKey)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/opds", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("apiKey in a multipart body = %d, want 401", rec.Code)
	}
	if req.MultipartForm != nil {
		t.Fatal("the body was parsed before authentication")
	}
}
