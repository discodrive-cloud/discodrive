package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapTokenFileSecurity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "setup-token")
	token, created, err := readOrCreateSetupToken(path)
	if err != nil || !created || len(token) != 64 {
		t.Fatalf("creation: %v", err)
	}
	again, created, err := readOrCreateSetupToken(path)
	if err != nil || created || again != token {
		t.Fatal("token did not survive restart")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readOrCreateSetupToken(path); err == nil {
		t.Fatal("world-readable token accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readOrCreateSetupToken(link); err == nil {
		t.Fatal("symlink token accepted")
	}
}
