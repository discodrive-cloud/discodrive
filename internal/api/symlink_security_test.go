package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestStreamFileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bob"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "alice"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bob", "secret"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../bob/secret", filepath.Join(root, "alice", "leak")); err != nil {
		t.Fatal(err)
	}
	s := &Server{storageRoot: root}
	rec := httptest.NewRecorder()
	s.streamFile(rec, httptest.NewRequest("GET", "/", nil), pgtype.Text{String: "text/plain", Valid: true}, "leak", "alice/leak")
	if rec.Code != 404 {
		t.Fatalf("symlink served: %d %s", rec.Code, rec.Body.String())
	}
}
