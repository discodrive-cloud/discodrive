package ebook

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

// setupDB spins up a Postgres testcontainer, runs migrations, and returns a
// query set + context. Skips the test if Docker is unavailable.
func setupDB(t *testing.T) (*db.Queries, context.Context) {
	t.Helper()
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return db.New(pool), ctx
}

// makeTenant creates a tenant + user and returns the user UUID string.
func makeTenant(t *testing.T, q *db.Queries, ctx context.Context) string {
	t.Helper()
	tenant, err := q.CreateTenant(ctx, "t")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	u, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID:     tenant.ID,
		Email:        "ebook-scanner-test@example.com",
		PasswordHash: "x",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return db.UUIDString(u.ID)
}

// TestScanFolderIndexesBook creates a folder + file node pointing at a real EPUB,
// runs ScanFolder, and asserts that the book row, authors, tags, and cover file
// are all correctly persisted. Re-scan returns 0 (change-gate). RemoveNode
// deletes the book row and the cover file.
func TestScanFolderIndexesBook(t *testing.T) {
	q, ctx := setupDB(t)
	userID := makeTenant(t, q, ctx)

	storageRoot := t.TempDir()
	ix := NewIndexer(q, storageRoot)

	uid, _ := db.ParseUUID(userID)

	// Create the books subdirectory on disk under storageRoot.
	booksDir := filepath.Join(storageRoot, "books")
	if err := os.MkdirAll(booksDir, 0o755); err != nil {
		t.Fatalf("mkdir books: %v", err)
	}

	// Build an EPUB on disk (reuse the test helper from epub_test.go,
	// but we need it at a fixed path under storageRoot).
	epubPath := filepath.Join(booksDir, "test.epub")
	buildEPUBAt(t, epubPath)

	// Create a directory node for the folder.
	folderNode, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		Name:     "books",
		IsDir:    true,
		DiskPath: pgtype.Text{String: "books", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode (folder): %v", err)
	}

	// Create a file node pointing at the EPUB.
	fileNode, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		ParentID: folderNode.ID,
		Name:     "test.epub",
		IsDir:    false,
		DiskPath: pgtype.Text{String: "books/test.epub", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode (file): %v", err)
	}

	folderID := db.UUIDString(folderNode.ID)

	// --- First scan: must index 1 book ---
	count, err := ix.ScanFolder(ctx, userID, folderID)
	if err != nil {
		t.Fatalf("ScanFolder: %v", err)
	}
	if count != 1 {
		t.Errorf("ScanFolder count = %d, want 1", count)
	}

	// Book must exist in DB with the right title.
	book, err := q.GetBookByNode(ctx, fileNode.ID)
	if err != nil {
		t.Fatalf("GetBookByNode: %v", err)
	}
	if book.Title != "The Great Test Book" {
		t.Errorf("book.Title = %q, want %q", book.Title, "The Great Test Book")
	}

	// Authors must be recorded.
	authors, err := q.BookAuthors(ctx, book.ID)
	if err != nil {
		t.Fatalf("BookAuthors: %v", err)
	}
	if len(authors) != 2 {
		t.Errorf("len(authors) = %d, want 2", len(authors))
	}

	// Tags must be recorded.
	tags, err := q.BookTags(ctx, book.ID)
	if err != nil {
		t.Fatalf("BookTags: %v", err)
	}
	if len(tags) != 2 {
		t.Errorf("len(tags) = %d, want 2", len(tags))
	}

	// Cover file must exist on disk.
	if !book.CoverPath.Valid || book.CoverPath.String == "" {
		t.Fatal("book.CoverPath is empty, want a cover path")
	}
	coverAbs := filepath.Join(storageRoot, book.CoverPath.String)
	if _, serr := os.Stat(coverAbs); serr != nil {
		t.Errorf("cover file not found at %q: %v", coverAbs, serr)
	}

	// --- Second scan without file change: change-gate must return 0 ---
	count2, err := ix.ScanFolder(ctx, userID, folderID)
	if err != nil {
		t.Fatalf("ScanFolder (second): %v", err)
	}
	if count2 != 0 {
		t.Errorf("second ScanFolder count = %d, want 0 (change-gate)", count2)
	}

	// --- RemoveNode: book gone, cover file removed ---
	nodeIDStr := db.UUIDString(fileNode.ID)
	if err := ix.RemoveNode(ctx, nodeIDStr); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}

	_, err = q.GetBookByNode(ctx, fileNode.ID)
	if err == nil {
		t.Error("GetBookByNode after RemoveNode: expected error (book deleted), got nil")
	}

	if _, serr := os.Stat(coverAbs); !os.IsNotExist(serr) {
		t.Errorf("cover file still exists after RemoveNode: %q", coverAbs)
	}
}

// TestIndexNodeUpserts verifies that calling IndexNode twice does not duplicate
// book rows and that cover_path is set on the first call.
func TestIndexNodeUpserts(t *testing.T) {
	q, ctx := setupDB(t)
	userID := makeTenant(t, q, ctx)

	storageRoot := t.TempDir()
	ix := NewIndexer(q, storageRoot)

	uid, _ := db.ParseUUID(userID)

	epubPath := filepath.Join(storageRoot, "book.epub")
	buildEPUBAt(t, epubPath)

	node, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		Name:     "book.epub",
		IsDir:    false,
		DiskPath: pgtype.Text{String: "book.epub", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	nodeID := db.UUIDString(node.ID)

	// First index.
	if err := ix.IndexNode(ctx, userID, nodeID, epubPath); err != nil {
		t.Fatalf("IndexNode (first): %v", err)
	}
	book1, err := q.GetBookByNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("GetBookByNode after first index: %v", err)
	}
	if book1.Title != "The Great Test Book" {
		t.Errorf("Title = %q, want %q", book1.Title, "The Great Test Book")
	}
	if !book1.CoverPath.Valid || book1.CoverPath.String == "" {
		t.Error("CoverPath not set after first IndexNode")
	}

	// Second index — must not duplicate rows.
	if err := ix.IndexNode(ctx, userID, nodeID, epubPath); err != nil {
		t.Fatalf("IndexNode (second): %v", err)
	}
	book2, err := q.GetBookByNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("GetBookByNode after second index: %v", err)
	}
	if book1.ID != book2.ID {
		t.Errorf("book ID changed on second upsert: %v → %v", book1.ID, book2.ID)
	}

	// Authors and tags must remain 2 each (not doubled).
	authors, _ := q.BookAuthors(ctx, book2.ID)
	if len(authors) != 2 {
		t.Errorf("authors after second upsert = %d, want 2", len(authors))
	}
	tags, _ := q.BookTags(ctx, book2.ID)
	if len(tags) != 2 {
		t.Errorf("tags after second upsert = %d, want 2", len(tags))
	}
}

// buildEPUBAt writes the same minimal EPUB used in epub_test.go to a given path.
// This is a duplicate of buildMinimalEPUB but writes to a caller-supplied path
// (not a temp file chosen by the OS), which is required for placing it under
// storageRoot at a known relative path.
func buildEPUBAt(t *testing.T, dst string) {
	t.Helper()

	// Reuse the shared epub builder but copy the result to dst.
	srcPath := buildMinimalEPUB(t)
	data, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read temp epub: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write epub to %q: %v", dst, err)
	}
}

// A cached cover occupies disk on the user's behalf, so it counts as their used space:
// otherwise small archives with large first pages would park data past the quota.
func TestBookCoverCountsTowardQuota(t *testing.T) {
	q, ctx := setupDB(t)
	userID := makeTenant(t, q, ctx)
	storageRoot := t.TempDir()
	ix := NewIndexer(q, storageRoot)
	uid, _ := db.ParseUUID(userID)

	epubPath := filepath.Join(storageRoot, "book.epub")
	buildEPUBAt(t, epubPath)
	node, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID: uid, Name: "book.epub", DiskPath: pgtype.Text{String: "book.epub", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	before, err := q.UserStorageUsage(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.IndexNode(ctx, userID, db.UUIDString(node.ID), epubPath); err != nil {
		t.Fatalf("IndexNode: %v", err)
	}
	book, err := q.GetBookByNode(ctx, node.ID)
	if err != nil || !book.CoverPath.Valid {
		t.Fatalf("book has no cover (err %v)", err)
	}
	st, err := os.Stat(filepath.Join(storageRoot, book.CoverPath.String))
	if err != nil {
		t.Fatal(err)
	}
	after, err := q.UserStorageUsage(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if after-before != st.Size() {
		t.Errorf("a %d-byte cover added %d bytes to the user's usage", st.Size(), after-before)
	}
	total, err := q.TotalStorageUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total < st.Size() {
		t.Errorf("server-wide usage %d is below the cover's %d bytes", total, st.Size())
	}
}
