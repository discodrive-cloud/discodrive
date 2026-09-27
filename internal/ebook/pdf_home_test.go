package ebook

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// pdfcpu used to create a config directory under $HOME on first use and abort the whole
// process when it could not (a service user without a writable home, a read-only
// container root). The scan runs in the background worker, so the server crash-looped.
// Runs in a child process: pdfcpu caches its configuration globally.
func TestParsePDFWithoutWritableHome(t *testing.T) {
	if os.Getenv("DD_PDF_CHILD") != "" {
		if _, err := parsePDF(os.Getenv("DD_PDF_CHILD")); err != nil {
			t.Fatalf("parsePDF: %v", err)
		}
		return
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })

	cmd := exec.Command(os.Args[0], "-test.run=^TestParsePDFWithoutWritableHome$")
	cmd.Env = append(os.Environ(),
		"DD_PDF_CHILD="+writeTempPDF(t, minimalPDFWithInfo, ".pdf"),
		"HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("parsing a PDF without a writable home failed: %v\n%s", err, out)
	}
}
