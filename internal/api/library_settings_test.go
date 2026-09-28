package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/music/musictest"
	"discodrive/internal/storage"
)

// librarySettingsServer is modTimeServer with the music settings route, also returning
// the user's id.
func librarySettingsServer(t *testing.T) (*Server, func(*http.Request) *httptest.ResponseRecorder, pgtype.UUID) {
	t.Helper()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{auth: svc, q: q, files: storage.NewFileService(pool, storage.NewLocalDisk(root)), storageRoot: root}
	tok, u, err := svc.Register(context.Background(), "lib@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /me/music", s.handlePutMusicSettings)
	do := func(req *http.Request) *httptest.ResponseRecorder {
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		svc.Middleware(mux).ServeHTTP(rec, req)
		return rec
	}
	return s, do, u.ID
}

// pushTrack uploads a synthesized mp3 titled after its file name into folder.
func pushTrack(t *testing.T, s *Server, uid pgtype.UUID, folder db.Node, name string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	musictest.SynthesizeAudio(t, src, name, "Artist", "Album", "mp3")
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fid := db.UUIDString(folder.ID)
	if _, err := s.files.Push(context.Background(), db.UUIDString(uid), &fid, name, nil, "", f); err != nil {
		t.Fatal(err)
	}
}

// Switching the music folder drops the old folder's tracks and indexes the new one; the
// cursor starts at the present, taken before the scan.
func TestMusicFolderSwitch(t *testing.T) {
	musictest.RequireFFmpeg(t)
	s, do, uid := librarySettingsServer(t) // like modTimeServer, plus PUT /me/music
	ctx := context.Background()
	a, _ := s.files.CreateFolder(ctx, db.UUIDString(uid), nil, "A")
	b, _ := s.files.CreateFolder(ctx, db.UUIDString(uid), nil, "B")
	pushTrack(t, s, uid, a, "a.mp3")
	pushTrack(t, s, uid, b, "b.mp3")

	put := func(folder db.Node) {
		body := fmt.Sprintf(`{"enabled":true,"folderNodeId":%q}`, db.UUIDString(folder.ID))
		req := httptest.NewRequest(http.MethodPut, "/me/music", strings.NewReader(body))
		if rec := do(req); rec.Code != http.StatusOK {
			t.Fatalf("PUT /me/music: %d %s", rec.Code, rec.Body)
		}
	}
	songsNamed := func() []string {
		songs, _ := s.q.AccessibleSongs(ctx, uid)
		var out []string
		for _, sg := range songs {
			out = append(out, sg.Title)
		}
		return out
	}
	waitFor := func(want []string) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if slices.Equal(songsNamed(), want) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("songs %v, want %v", songsNamed(), want)
	}

	put(a)
	waitFor([]string{"a.mp3"})
	seqBefore, _ := s.q.GetUserChangeSeq(ctx, uid)
	put(b)
	waitFor([]string{"b.mp3"})
	ms, _ := s.q.GetMusicSettings(ctx, uid)
	if ms.IndexedSeq != seqBefore {
		t.Fatalf("cursor %d, want %d (the change seq when the folder was switched)", ms.IndexedSeq, seqBefore)
	}
}
