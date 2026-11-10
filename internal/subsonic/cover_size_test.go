package subsonic

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// An album grid asks for covers at a size. It gets a thumbnail of that order, made once
// and revalidated by ETag afterwards — not the full-size picture every time.
func TestGetCoverArtHonoursSize(t *testing.T) {
	h, ctx := setupSubsonic(t)
	h.storageRoot = t.TempDir()
	h.xaccel = false
	userUUID := mustUserID(t, ctx, h.q, testEmail)

	img := image.NewRGBA(image.Rect(0, 0, 1400, 1400))
	for y := 0; y < 1400; y++ {
		for x := 0; x < 1400; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var full bytes.Buffer
	if err := jpeg.Encode(&full, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	rel := "covers/big-cover.jpg"
	if err := os.MkdirAll(filepath.Join(h.storageRoot, "covers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.storageRoot, rel), full.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	coverNode, err := h.q.CreateNode(ctx, db.CreateNodeParams{
		UserID: userUUID, Name: "big-cover.jpg", DiskPath: pgtype.Text{String: rel, Valid: true},
		Mime:        pgtype.Text{String: "image/jpeg", Valid: true},
		ContentHash: pgtype.Text{String: "5226a1ddeecf8587f1ab83f254e44c5d64ccb55a95ab1d911ef76b6838bf6b41", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode(cover): %v", err)
	}
	songNode := makeNode(t, ctx, h.q, userUUID, pgtype.UUID{}, "sized-song.mp3", false)
	_, album, _ := seed(t, ctx, h.q, userUUID, songNode, "Sized Artist", "Sized Album", "Sized Song")
	if err := h.q.SetAlbumCover(ctx, db.SetAlbumCoverParams{
		ID: album.ID, CoverArt: pgtype.Text{String: db.UUIDString(coverNode.ID), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	id := "id=" + encID("al", db.UUIDString(album.ID))

	rec := doGetRaw(h, testAPIKey, "getCoverArt", id+"&size=300")
	if rec.Code != http.StatusOK {
		t.Fatalf("size=300: %d %s", rec.Code, rec.Body.String())
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	if cfg.Width != 320 || cfg.Height != 320 {
		t.Errorf("size=300 gave %dx%d, want the 320 px thumbnail", cfg.Width, cfg.Height)
	}
	if rec.Body.Len()*4 > full.Len() {
		t.Errorf("thumbnail is %d bytes of a %d-byte cover", rec.Body.Len(), full.Len())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the thumbnail")
	}

	again := httptest.NewRequest(http.MethodGet, "/rest/getCoverArt?apiKey="+testAPIKey+"&f=json&c=test&v=1.16.1&"+id+"&size=300", nil)
	again.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, again)
	if rec.Code != http.StatusNotModified {
		t.Errorf("revalidation: %d, want 304", rec.Code)
	}

	// Without a size the cover comes whole, as before.
	rec = doGetRaw(h, testAPIKey, "getCoverArt", id)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), full.Bytes()) {
		t.Errorf("no size: %d, %d bytes (want the %d-byte file)", rec.Code, rec.Body.Len(), full.Len())
	}
}
