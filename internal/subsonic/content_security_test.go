package subsonic

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/music"
)

func mp3WithPicture(mime string, body []byte) []byte {
	picture := append([]byte{0}, []byte(mime)...)
	picture = append(picture, 0, 3, 0)
	picture = append(picture, body...)
	frame := make([]byte, 10)
	copy(frame, "APIC")
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(picture)))
	frame = append(frame, picture...)
	header := []byte{'I', 'D', '3', 3, 0, 0, 0, 0, 0, 0}
	n := len(frame)
	for i := 9; i >= 6; i-- {
		header[i] = byte(n & 127)
		n >>= 7
	}
	return append(append(header, frame...), make([]byte, 2048)...)
}

func TestEmbeddedCoverRejectsActiveContent(t *testing.T) {
	for _, mime := range []string{"text/html", "application/javascript"} {
		p := filepath.Join(t.TempDir(), "probe.mp3")
		payload := []byte("review_active_content")
		if err := os.WriteFile(p, mp3WithPicture(mime, payload), 0600); err != nil {
			t.Fatal(err)
		}
		b, ct, ok := music.EmbeddedCover(p)
		if ok || ct != "" || len(b) != 0 {
			t.Fatalf("unsafe response: ok=%v type=%q body=%q", ok, ct, b)
		}
	}
}

func TestCoverEndpointRejectsHTML(t *testing.T) {
	h, ctx, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()
	uid := mustUserID(t, ctx, h.q, testEmail)
	payload := []byte("<!doctype html><title>review proof</title><script src='/rest/getCoverArt.view?id=SECOND&apiKey=OWN_KEY'></script>")
	song := seedSongWithFile(t, ctx, h.q, uid, h.storageRoot, mp3WithPicture("text/html", payload))
	if err := h.q.SetAlbumCover(ctx, db.SetAlbumCoverParams{ID: song.AlbumID, CoverArt: pgtype.Text{String: db.UUIDString(song.NodeID), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/rest/getCoverArt.view?id="+encID("tr", db.UUIDString(song.ID))+"&apiKey="+testAPIKey, nil))
	if rec.Code != 404 || bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("unsafe response: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
}

func TestStoredMediaRejectsActiveContent(t *testing.T) {
	for _, accel := range []bool{false, true} {
		for _, ct := range []string{"text/html", "application/javascript", "image/svg+xml", ""} {
			h := &Handler{xaccel: accel}
			rec := httptest.NewRecorder()
			h.serveNodeFile(&reqCtx{w: rec, r: httptest.NewRequest("GET", "/rest/stream", nil)}, "unused", "episode", ct)
			if rec.Code != 403 || rec.Header().Get("X-Accel-Redirect") != "" {
				t.Fatalf("unsafe stored response: accel=%v ct=%s status=%d", accel, ct, rec.Code)
			}
		}
	}
}

func TestEmbeddedCoverUsesImageBytesInsteadOfMetadata(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "cover.mp3")
	if err := os.WriteFile(p, mp3WithPicture("text/html", pngData.Bytes()), 0600); err != nil {
		t.Fatal(err)
	}
	data, ct, ok := music.EmbeddedCover(p)
	if !ok || ct != "image/png" || !bytes.Equal(data, pngData.Bytes()) {
		t.Fatalf("valid raster rejected or trusted metadata: type=%s", ct)
	}
}
