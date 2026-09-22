package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// streamEnv is a user with one folder and one on-disk media file, plus a Server
// wired to a real FileService — everything the stream endpoint touches.
type streamEnv struct {
	ctx     context.Context
	q       *db.Queries
	svc     *auth.Service
	files   *storage.FileService
	s       *Server
	root    string
	userID  string
	user    db.User
	folder  db.Node
	track   db.Node
	content []byte
}

func buildStreamEnv(t *testing.T, email string) *streamEnv {
	t.Helper()
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	fileSvc := storage.NewFileService(pool, storage.NewLocalDisk(root))
	s := &Server{auth: svc, q: q, files: fileSvc, storageRoot: root}

	_, user, err := svc.Register(ctx, email, "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	uid := user.ID
	userID := db.UUIDString(uid)

	relDir := userID + "/media"
	if err := os.MkdirAll(filepath.Join(root, relDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	folder, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID: uid, Name: "media", IsDir: true,
		DiskPath: pgtype.Text{String: relDir, Valid: true},
	})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	content := []byte("fake mp3 payload, long enough to slice a range from")
	relFile := relDir + "/track.mp3"
	if err := os.WriteFile(filepath.Join(root, relFile), content, 0o644); err != nil {
		t.Fatalf("write track: %v", err)
	}
	track, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID: uid, ParentID: folder.ID, Name: "track.mp3", IsDir: false,
		Size:     pgtype.Int8{Int64: int64(len(content)), Valid: true},
		DiskPath: pgtype.Text{String: relFile, Valid: true},
		Mime:     pgtype.Text{String: "audio/mpeg", Valid: true},
	})
	if err != nil {
		t.Fatalf("create track: %v", err)
	}
	return &streamEnv{ctx: ctx, q: q, svc: svc, files: fileSvc, s: s, root: root,
		userID: userID, user: user, folder: folder, track: track, content: content}
}

func (e *streamEnv) mint(t *testing.T, nodeID string) string {
	t.Helper()
	minter, err := e.svc.StreamMinter(e.ctx, e.userID)
	if err != nil {
		t.Fatalf("StreamMinter: %v", err)
	}
	tok, err := minter(nodeID)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok
}

func (e *streamEnv) streamReq(nodeID, token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/files/"+nodeID+"/stream?t="+token, nil)
	r.SetPathValue("id", nodeID)
	return r
}

func TestStreamHappyPathAndRange(t *testing.T) {
	e := buildStreamEnv(t, "stream1@x.test")
	nid := db.UUIDString(e.track.ID)
	tok := e.mint(t, nid)

	rec := httptest.NewRecorder()
	e.s.handleStream(rec, e.streamReq(nid, tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != string(e.content) {
		t.Fatalf("body mismatch: %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("Content-Type=%q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "inline; filename*=UTF-8''track.mp3" {
		t.Fatalf("Content-Disposition=%q", cd)
	}
	if ns := rec.Header().Get("X-Content-Type-Options"); ns != "nosniff" {
		t.Fatalf("nosniff missing, got %q", ns)
	}

	// Seeking works: Range requests come back 206 with the requested slice.
	rec = httptest.NewRecorder()
	req := e.streamReq(nid, tok)
	req.Header.Set("Range", "bytes=5-9")
	e.s.handleStream(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range code=%d", rec.Code)
	}
	if got := rec.Body.String(); got != string(e.content[5:10]) {
		t.Fatalf("range body=%q", got)
	}
}

func TestStreamRejectsBadTokens(t *testing.T) {
	e := buildStreamEnv(t, "stream2@x.test")
	nid := db.UUIDString(e.track.ID)

	// A full session JWT pasted into ?t= must not stream: purpose is empty.
	sessionTok, err := e.svc.Login(e.ctx, "stream2@x.test", "password12")
	if err != nil || sessionTok.Token == "" {
		t.Fatalf("login: %v", err)
	}
	// A stream token for a DIFFERENT node must not open this one.
	otherNode, err := e.q.CreateNode(e.ctx, db.CreateNodeParams{
		UserID: e.user.ID, ParentID: e.folder.ID, Name: "other.mp3", IsDir: false,
		DiskPath: pgtype.Text{String: e.userID + "/media/other.mp3", Valid: true},
		Mime:     pgtype.Text{String: "audio/mpeg", Valid: true},
	})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	// An expired stream token (signed with the same secret as bootstrapPairingDB's issuer).
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{
		Ver: e.user.TokenVersion, Pur: "stream-v2", Nid: nid,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   e.userID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		},
	}).SignedString([]byte("secret"))
	if err != nil {
		t.Fatalf("sign expired: %v", err)
	}

	for name, tok := range map[string]string{
		"session JWT":      sessionTok.Token,
		"empty":            "",
		"garbage":          "not-a-jwt",
		"wrong node scope": e.mint(t, db.UUIDString(otherNode.ID)),
		"expired":          expired,
	} {
		rec := httptest.NewRecorder()
		e.s.handleStream(rec, e.streamReq(nid, tok))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

func TestStreamMimeAllowlist(t *testing.T) {
	e := buildStreamEnv(t, "stream3@x.test")

	rel := e.userID + "/media/notes.html"
	if err := os.WriteFile(filepath.Join(e.root, rel), []byte("<script>boom</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, mime := range map[string]pgtype.Text{
		"non-media mime": {String: "text/html", Valid: true},
		"missing mime":   {},
	} {
		node, err := e.q.CreateNode(e.ctx, db.CreateNodeParams{
			UserID: e.user.ID, ParentID: e.folder.ID, Name: "n-" + name, IsDir: false,
			DiskPath: pgtype.Text{String: rel, Valid: true}, Mime: mime,
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		nid := db.UUIDString(node.ID)
		rec := httptest.NewRecorder()
		e.s.handleStream(rec, e.streamReq(nid, e.mint(t, nid)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: expected 403, got %d", name, rec.Code)
		}
	}
}

func TestStreamFolderIsNotAFile(t *testing.T) {
	e := buildStreamEnv(t, "stream4@x.test")
	nid := db.UUIDString(e.folder.ID)
	rec := httptest.NewRecorder()
	e.s.handleStream(rec, e.streamReq(nid, e.mint(t, nid)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for folder, got %d", rec.Code)
	}
}

// TestStreamRevocation: a stream URL dies with the session epoch (password change)
// and with share removal — revocation is live, not deferred to token expiry.
func TestStreamRevocation(t *testing.T) {
	e := buildStreamEnv(t, "owner@x.test")
	nid := db.UUIDString(e.track.ID)

	// Grantee gets access via a folder share, mints their own stream token.
	_, grantee, err := e.svc.Register(e.ctx, "grantee@x.test", "password12")
	if err != nil {
		t.Fatalf("register grantee: %v", err)
	}
	share, err := e.files.ShareToUser(e.ctx, e.userID, db.UUIDString(e.folder.ID), "grantee@x.test", "read", nil)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	granteeMint, err := e.svc.StreamMinter(e.ctx, db.UUIDString(grantee.ID))
	if err != nil {
		t.Fatalf("grantee minter: %v", err)
	}
	granteeTok, err := granteeMint(nid)
	if err != nil {
		t.Fatalf("grantee mint: %v", err)
	}

	rec := httptest.NewRecorder()
	e.s.handleStream(rec, e.streamReq(nid, granteeTok))
	if rec.Code != http.StatusOK {
		t.Fatalf("grantee stream before revoke: %d (%s)", rec.Code, rec.Body.String())
	}

	// Owner revokes the share → the still-valid token stops working immediately.
	if err := e.files.Revoke(e.ctx, e.userID, db.UUIDString(share.ID)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	rec = httptest.NewRecorder()
	e.s.handleStream(rec, e.streamReq(nid, granteeTok))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("grantee stream after revoke: expected 404, got %d", rec.Code)
	}

	// Owner's own token dies on password change (token_version bump).
	ownerTok := e.mint(t, nid)
	if _, err := e.svc.ChangePassword(e.ctx, e.userID, "password12", "newpassword12"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	rec = httptest.NewRecorder()
	e.s.handleStream(rec, e.streamReq(nid, ownerTok))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("owner stream after password change: expected 401, got %d", rec.Code)
	}
}

func TestStreamXAccelMode(t *testing.T) {
	e := buildStreamEnv(t, "stream5@x.test")
	e.s.xaccel = true
	nid := db.UUIDString(e.track.ID)

	rec := httptest.NewRecorder()
	e.s.handleStream(rec, e.streamReq(nid, e.mint(t, nid)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if got := rec.Header().Get("X-Accel-Redirect"); got != "/__data/"+e.userID+"/media/track.mp3" {
		t.Fatalf("X-Accel-Redirect=%q", got)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "inline; filename*=UTF-8''track.mp3" {
		t.Fatalf("Content-Disposition=%q", cd)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body must be empty in X-Accel mode, got %d bytes", rec.Body.Len())
	}
}
