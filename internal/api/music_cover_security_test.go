package api

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/music/tagwrite"
)

func TestMusicTagCoverRejectsActiveContent(t *testing.T) {
	tok, nid, s := buildTagEditorEnv(t, "cover-security@example.test")
	id, _ := db.ParseUUID(nid)
	node, err := s.q.GetNode(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	writer, _ := tagwrite.For("mp3")
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		data   []byte
		status int
	}{
		{[]byte("<!doctype html><script>alert(1)</script>"), 404},
		{pngData.Bytes(), 200},
	} {
		if err := writer.Apply(filepath.Join(s.storageRoot, node.DiskPath.String), tagwrite.Tags{}, tagwrite.CoverReplace, &tagwrite.Cover{Data: tc.data, Mime: "text/html"}); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/me/music/tags/"+nid+"/cover", nil)
		req.SetPathValue("id", nid)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		s.auth.Middleware(http.HandlerFunc(s.handleGetMusicTagsCover)).ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("status=%d want=%d", rec.Code, tc.status)
		}
		if tc.status == 200 && (rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("X-Content-Type-Options") != "nosniff") {
			t.Fatal("untrusted metadata reached response headers")
		}
	}
}
