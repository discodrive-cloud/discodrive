package opds

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// minimal valid 1x1 PNG bytes (67 bytes).
var fakePNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk length + type
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // width=1, height=1
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, // 8-bit RGB, CRC
	0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, // IDAT chunk
	0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, // compressed pixel data
	0x00, 0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc, // CRC
	0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, // IEND chunk length + type
	0x44, 0xae, 0x42, 0x60, 0x82, // IEND CRC
}

// seedCoverBooks creates:
//   - user1's book WITH a cover (cover_path set, image file written to storageRoot)
//   - user1's book WITHOUT a cover (cover_path null)
//   - user2's book WITH a cover, NOT shared with user1
//
// Returns (bookWithCoverID, bookNoCoverID, user2BookID).
func seedCoverBooks(t *testing.T, h *Handler) (bookWithCoverID, bookNoCoverID, user2BookID string) {
	t.Helper()
	ctx := t.Context()

	// Write the cover image to the temp storageRoot.
	coverRelPath := ".covers/ebooks/test-cover.png"
	coverAbsPath := filepath.Join(h.storageRoot, coverRelPath)
	if err := os.MkdirAll(filepath.Dir(coverAbsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll cover dir: %v", err)
	}
	if err := os.WriteFile(coverAbsPath, fakePNG, 0o644); err != nil {
		t.Fatalf("WriteFile cover: %v", err)
	}

	user1, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatalf("GetUserByEmail user1: %v", err)
	}

	// Book WITH cover.
	node1, err := h.q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   user1.ID,
		ParentID: pgtype.UUID{Valid: false},
		Name:     "book-with-cover.epub",
		IsDir:    false,
		DiskPath: pgtype.Text{String: "books/book-with-cover.epub", Valid: true},
		Size:     pgtype.Int8{Int64: 1000, Valid: true},
		Mime:     pgtype.Text{String: "application/epub+zip", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode node1: %v", err)
	}
	book1, err := h.q.UpsertBook(ctx, db.UpsertBookParams{
		UserID:      user1.ID,
		NodeID:      node1.ID,
		Title:       "Book With Cover",
		SortTitle:   "book with cover",
		Format:      "epub",
		ContentType: "application/epub+zip",
		Size:        pgtype.Int8{Int64: 1000, Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertBook book1: %v", err)
	}
	if err := h.q.SetBookCoverPath(ctx, db.SetBookCoverPathParams{
		ID:        book1.ID,
		CoverPath: pgtype.Text{String: coverRelPath, Valid: true},
	}); err != nil {
		t.Fatalf("SetBookCoverPath book1: %v", err)
	}

	// Book WITHOUT cover (cover_path stays NULL).
	node2, err := h.q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   user1.ID,
		ParentID: pgtype.UUID{Valid: false},
		Name:     "book-no-cover.epub",
		IsDir:    false,
		DiskPath: pgtype.Text{String: "books/book-no-cover.epub", Valid: true},
		Size:     pgtype.Int8{Int64: 500, Valid: true},
		Mime:     pgtype.Text{String: "application/epub+zip", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode node2: %v", err)
	}
	book2, err := h.q.UpsertBook(ctx, db.UpsertBookParams{
		UserID:      user1.ID,
		NodeID:      node2.ID,
		Title:       "Book No Cover",
		SortTitle:   "book no cover",
		Format:      "epub",
		ContentType: "application/epub+zip",
		Size:        pgtype.Int8{Int64: 500, Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertBook book2: %v", err)
	}

	// user2's book with its own cover, NOT shared with user1.
	tenant2, err := h.q.CreateTenant(ctx, "cv-tenant2")
	if err != nil {
		t.Fatalf("CreateTenant user2: %v", err)
	}
	user2, err := h.q.CreateUser(ctx, db.CreateUserParams{
		TenantID:     tenant2.ID,
		Email:        "cv-user2@x.test",
		PasswordHash: "irrelevant",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("CreateUser user2: %v", err)
	}
	node3, err := h.q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   user2.ID,
		ParentID: pgtype.UUID{Valid: false},
		Name:     "secret-book.epub",
		IsDir:    false,
		DiskPath: pgtype.Text{String: "books/secret-book.epub", Valid: true},
		Size:     pgtype.Int8{Int64: 200, Valid: true},
		Mime:     pgtype.Text{String: "application/epub+zip", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode node3: %v", err)
	}
	book3, err := h.q.UpsertBook(ctx, db.UpsertBookParams{
		UserID:      user2.ID,
		NodeID:      node3.ID,
		Title:       "User2 Secret Book",
		SortTitle:   "user2 secret book",
		Format:      "epub",
		ContentType: "application/epub+zip",
		Size:        pgtype.Int8{Int64: 200, Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertBook book3: %v", err)
	}
	if err := h.q.SetBookCoverPath(ctx, db.SetBookCoverPathParams{
		ID:        book3.ID,
		CoverPath: pgtype.Text{String: coverRelPath, Valid: true},
	}); err != nil {
		t.Fatalf("SetBookCoverPath book3: %v", err)
	}

	return db.UUIDString(book1.ID), db.UUIDString(book2.ID), db.UUIDString(book3.ID)
}

// setupOPDSWithCoverStorage creates a Handler with a real temp storageRoot.
// Re-uses setupOPDSWithStorage from download_test.go if called from same package.
func setupOPDSWithCoverStorage(t *testing.T) *Handler {
	t.Helper()
	h, _ := setupOPDS(t)
	h.storageRoot = t.TempDir()
	return h
}

// TestCoverOwnBookWithCover verifies that user1 can fetch a cover for their own book (200)
// and that the response body matches the image file bytes exactly.
func TestCoverOwnBookWithCover(t *testing.T) {
	h := setupOPDSWithCoverStorage(t)
	bookWithCoverID, _, _ := seedCoverBooks(t, h)

	rec := opdsGetBasic(h, "/opds/cover/"+bookWithCoverID, testEmail, testPassword)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, fakePNG) {
		t.Errorf("cover body mismatch: got %d bytes, want %d bytes", len(got), len(fakePNG))
	}
}

// The thumbnail endpoint returns the cover scaled down for a catalog row, not the full
// picture under another name.
func TestCoverThumbnailIsScaledDown(t *testing.T) {
	h := setupOPDSWithCoverStorage(t)
	bookWithCoverID, _, _ := seedCoverBooks(t, h)

	// A cover of a realistic size in place of the 1x1 fixture.
	img := image.NewRGBA(image.Rect(0, 0, 1000, 1500))
	for y := 0; y < 1500; y++ {
		for x := 0; x < 1000; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var full bytes.Buffer
	if err := png.Encode(&full, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.storageRoot, ".covers/ebooks/test-cover.png"), full.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	recFull := opdsGetBasic(h, "/opds/cover/"+bookWithCoverID, testEmail, testPassword)
	if recFull.Code != http.StatusOK || !bytes.Equal(recFull.Body.Bytes(), full.Bytes()) {
		t.Fatalf("full cover: %d, %d bytes (want the %d-byte file)", recFull.Code, recFull.Body.Len(), full.Len())
	}
	for i := 0; i < 2; i++ { // made, then served from the cache
		rec := opdsGetBasic(h, "/opds/cover/"+bookWithCoverID+"/thumbnail", testEmail, testPassword)
		if rec.Code != http.StatusOK {
			t.Fatalf("thumbnail: expected 200, got %d", rec.Code)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("thumbnail is not a JPEG: %v", err)
		}
		if cfg.Height != 320 || cfg.Width > 320 {
			t.Errorf("thumbnail is %dx%d, want 320 on the long side", cfg.Width, cfg.Height)
		}
		if rec.Body.Len() >= full.Len() {
			t.Errorf("thumbnail (%d bytes) is not smaller than the cover (%d bytes)", rec.Body.Len(), full.Len())
		}
	}
}

// TestCoverBookWithoutCover verifies that a book with no cover_path returns 404.
func TestCoverBookWithoutCover(t *testing.T) {
	h := setupOPDSWithCoverStorage(t)
	_, bookNoCoverID, _ := seedCoverBooks(t, h)

	rec := opdsGetBasic(h, "/opds/cover/"+bookNoCoverID, testEmail, testPassword)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for book without cover, got %d", rec.Code)
	}
}

// TestCoverOtherUserBook verifies that user1 cannot access user2's book cover (404, no leak).
func TestCoverOtherUserBook(t *testing.T) {
	h := setupOPDSWithCoverStorage(t)
	_, _, user2BookID := seedCoverBooks(t, h)

	rec := opdsGetBasic(h, "/opds/cover/"+user2BookID, testEmail, testPassword)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for other user's book, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestCoverRandomUUID verifies that a non-existent book UUID returns 404.
func TestCoverRandomUUID(t *testing.T) {
	h := setupOPDSWithCoverStorage(t)
	_, _, _ = seedCoverBooks(t, h)

	rec := opdsGetBasic(h, "/opds/cover/00000000-0000-0000-0000-000000000000", testEmail, testPassword)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown uuid, got %d", rec.Code)
	}
}

// TestCoverInvalidBookID verifies that a non-UUID path value returns 404.
func TestCoverInvalidBookID(t *testing.T) {
	h := setupOPDSWithCoverStorage(t)

	rec := opdsGetBasic(h, "/opds/cover/not-a-uuid", testEmail, testPassword)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-uuid, got %d", rec.Code)
	}
}
