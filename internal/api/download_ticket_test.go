package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// A browser saves a file by following a link, not by reading it into the page: the link
// carries a ticket for exactly that path, minted for the signed-in session.
func ticketMux(e *streamEnv) *http.ServeMux {
	mux := http.NewServeMux()
	e.s.ticketed(mux, "GET /files/{id}/content", http.HandlerFunc(e.s.handleDownload))
	mux.Handle("POST /me/download-url", e.svc.Middleware(http.HandlerFunc(e.s.handleDownloadURL)))
	mux.Handle("GET /me", e.svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	return mux
}

func downloadURL(t *testing.T, mux *http.ServeMux, bearer, path string) (string, int) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"path": path})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, authedReq(http.MethodPost, "/me/download-url", bearer, string(body), ""))
	if rec.Code != http.StatusOK {
		return "", rec.Code
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.URL == "" {
		t.Fatalf("download-url answer: %s", rec.Body.String())
	}
	return out.URL, rec.Code
}

func get(mux *http.ServeMux, url string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestDownloadTicketServesTheFile(t *testing.T) {
	e := buildStreamEnv(t, "ticket1@x.test")
	mux := ticketMux(e)
	bearer := e.login(t, "ticket1@x.test")
	path := "/files/" + db.UUIDString(e.track.ID) + "/content"

	url, code := downloadURL(t, mux, bearer, path)
	if code != http.StatusOK {
		t.Fatalf("download-url: %d", code)
	}
	rec := get(mux, url)
	if rec.Code != http.StatusOK || rec.Body.String() != string(e.content) {
		t.Fatalf("ticket download: %d %q", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "attachment; filename*=UTF-8''track.mp3" {
		t.Fatalf("Content-Disposition=%q", cd)
	}
	// Whoever holds the link holds one file for an hour, not a session that renews itself.
	if tok := rec.Header().Get("X-Token"); tok != "" {
		t.Fatalf("a ticket request renewed the session")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Fatalf("Cache-Control=%q", cc)
	}
	// A paused download resumes on the same link.
	rec = get(mux, url, "Range", "bytes=5-9")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != string(e.content[5:10]) {
		t.Fatalf("range: %d %q", rec.Code, rec.Body.String())
	}
	// The session header keeps working on the same route.
	if rec = get(mux, path, "Authorization", "Bearer "+bearer); rec.Code != http.StatusOK {
		t.Fatalf("bearer download: %d", rec.Code)
	}
	if rec = get(mux, path); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: %d", rec.Code)
	}
}

func TestDownloadTicketOpensOnlyItsPath(t *testing.T) {
	e := buildStreamEnv(t, "ticket2@x.test")
	mux := ticketMux(e)
	bearer := e.login(t, "ticket2@x.test")
	nid := db.UUIDString(e.track.ID)
	path := "/files/" + nid + "/content"

	other, err := e.q.CreateNode(e.ctx, db.CreateNodeParams{
		UserID: e.user.ID, ParentID: e.folder.ID, Name: "other.mp3",
		DiskPath: pgtype.Text{String: e.userID + "/media/track.mp3", Valid: true},
		Mime:     pgtype.Text{String: "audio/mpeg", Valid: true},
	})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	otherURL, _ := downloadURL(t, mux, bearer, "/files/"+db.UUIDString(other.ID)+"/content")
	_, otherTicket, _ := cutTicket(otherURL)

	for name, ticket := range map[string]string{
		"another file's ticket": otherTicket,
		"a stream token":        e.mint(t, nid),
		"a session token":       bearer,
		"garbage":               "not-a-jwt",
	} {
		if rec := get(mux, path+"?dl="+ticket); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, rec.Code)
		}
	}
	// A ticket is not a session: routes that take no tickets do not read it.
	url, _ := downloadURL(t, mux, bearer, path)
	_, ticket, _ := cutTicket(url)
	if rec := get(mux, "/me?dl="+ticket); rec.Code != http.StatusUnauthorized {
		t.Errorf("ticket on a session route: %d, want 401", rec.Code)
	}
	if rec := get(mux, "/me", "Authorization", "Bearer "+ticket); rec.Code != http.StatusUnauthorized {
		t.Errorf("ticket as a bearer: %d, want 401", rec.Code)
	}
	// Tickets are minted only for routes that take them.
	for _, p := range []string{"/me", "files/" + nid + "/content", path + "?x=1", "/files/" + nid + "/stream", ""} {
		if _, code := downloadURL(t, mux, bearer, p); code != http.StatusBadRequest {
			t.Errorf("download-url for %q: %d, want 400", p, code)
		}
	}
}

func TestDownloadTicketDiesWithItsSession(t *testing.T) {
	e := buildStreamEnv(t, "ticket3@x.test")
	mux := ticketMux(e)
	nid := db.UUIDString(e.track.ID)
	path := "/files/" + nid + "/content"

	// Someone the folder is shared with: the ticket is theirs, access is checked on use.
	if _, _, err := e.svc.Register(e.ctx, "ticket3b@x.test", "password12"); err != nil {
		t.Fatalf("register: %v", err)
	}
	share, err := e.files.ShareToUser(e.ctx, e.userID, db.UUIDString(e.folder.ID), "ticket3b@x.test", "read", nil)
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	guestURL, code := downloadURL(t, mux, e.login(t, "ticket3b@x.test"), path)
	if code != http.StatusOK {
		t.Fatalf("guest download-url: %d", code)
	}
	if rec := get(mux, guestURL); rec.Code != http.StatusOK {
		t.Fatalf("guest download: %d", rec.Code)
	}
	if err := e.files.Revoke(e.ctx, e.userID, db.UUIDString(share.ID)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if rec := get(mux, guestURL); rec.Code != http.StatusNotFound {
		t.Fatalf("guest download after the share is gone: %d, want 404", rec.Code)
	}

	ownerURL, _ := downloadURL(t, mux, e.login(t, "ticket3@x.test"), path)
	if rec := get(mux, ownerURL); rec.Code != http.StatusOK {
		t.Fatalf("owner download: %d", rec.Code)
	}
	if _, err := e.svc.ChangePassword(e.ctx, e.userID, "password12", "newpassword12"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if rec := get(mux, ownerURL); rec.Code != http.StatusUnauthorized {
		t.Fatalf("owner download after a password change: %d, want 401", rec.Code)
	}
}

func cutTicket(url string) (path, ticket string, ok bool) {
	for i := 0; i+4 <= len(url); i++ {
		if url[i:i+4] == "?dl=" {
			return url[:i], url[i+4:], true
		}
	}
	return url, "", false
}
