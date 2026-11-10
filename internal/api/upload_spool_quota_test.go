package api

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/quota"
)

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// A multipart upload is spooled to the temp directory before the quota is applied. A user
// with almost no quota could park gigabytes there with a handful of parallel requests; the
// server must stop reading at what the user may still store.
func TestMultipartUploadOverQuota_BodyIsNotSpooled(t *testing.T) {
	s, do := modTimeServer(t)
	s.files.SetQuota(quota.New(s.q, 0))
	ctx := context.Background()
	user, err := s.q.GetUserByEmail(ctx, "mtime@x.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.q.UpdateUser(ctx, db.UpdateUserParams{
		ID: user.ID, Role: user.Role, StorageQuota: pgtype.Int8{Int64: 1000, Valid: true},
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}

	const size = 16 << 20
	for _, tc := range []struct {
		name    string
		chunked bool
	}{{"declared length", false}, {"chunked", true}} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			fw, _ := mw.CreateFormFile("file", "big.bin")
			_, _ = io.CopyN(fw, zeros{}, size)
			_ = mw.Close()
			total := int64(body.Len())
			cr := &countingReader{r: &body}
			req := httptest.NewRequest(http.MethodPost, "/files/upload", cr)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			req.ContentLength = total
			if tc.chunked {
				req.ContentLength = -1
			}
			rec := do(req)
			if rec.Code != http.StatusInsufficientStorage {
				t.Errorf("code = %d, want 507 (%s)", rec.Code, rec.Body)
			}
			if cr.n > 2<<20 {
				t.Errorf("server read %d MiB of a %d MiB body from a user with 1000 bytes of quota", cr.n>>20, total>>20)
			}
		})
	}
}
