package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"discodrive/internal/db"
)

// A track shared on its own does not share its folder: the folder's cover.jpg
// stays private. Sharing the folder does share it.
func TestMediaCoverSiblingNeedsFolderAccess(t *testing.T) {
	e := buildStreamEnv(t, "coverowner@x.test")
	if err := os.WriteFile(filepath.Join(e.root, e.userID+"/media/cover.jpg"), []byte("\xff\xd8\xffsecret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Register(e.ctx, "coverviewer@x.test", "password12"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.files.ShareToUser(e.ctx, e.userID, db.UUIDString(e.track.ID), "coverviewer@x.test", "read", nil); err != nil {
		t.Fatalf("share file: %v", err)
	}
	viewer := e.login(t, "coverviewer@x.test")

	if rec := e.coverReq(t, viewer, db.UUIDString(e.track.ID)); rec.Code != http.StatusNotFound {
		t.Fatalf("file-only share: cover %d %q, want 404", rec.Code, rec.Body.String())
	}
	if rec := e.coverReq(t, e.login(t, "coverowner@x.test"), db.UUIDString(e.track.ID)); rec.Code != http.StatusOK {
		t.Fatalf("owner: %d", rec.Code)
	}

	if _, err := e.files.ShareToUser(e.ctx, e.userID, db.UUIDString(e.folder.ID), "coverviewer@x.test", "read", nil); err != nil {
		t.Fatalf("share folder: %v", err)
	}
	if rec := e.coverReq(t, viewer, db.UUIDString(e.track.ID)); rec.Code != http.StatusOK {
		t.Fatalf("folder share: cover %d, want 200", rec.Code)
	}
}
