package carddav

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	godavcarddav "github.com/emersion/go-webdav/carddav"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/dav"
	"discodrive/internal/db"
)

type reviewEnv struct {
	h                  http.Handler
	pool               *pgxpool.Pool
	svc                *dav.Service
	ownerID, sharee    string
	abURI, abID, abDir string
}

// setupReview: two users, the owner's address book, and a handler that dispatches the way
// Handler does (PROPPATCH, REPORT, raw GET, raw PUT body) with the user taken from X-User-ID.
func setupReview(t *testing.T) *reviewEnv {
	t.Helper()
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("Docker required: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, _ := pgxpool.New(ctx, dsn)
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	owner, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "owner@x", PasswordHash: "x", Role: "user"})
	sharee, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "sharee@x", PasswordHash: "x", Role: "user"})
	svc := dav.NewService(pool)
	ownerID := db.UUIDString(owner.ID)
	ab, err := svc.CreateAddressbook(ctx, ownerID, "Контакты")
	if err != nil {
		t.Fatalf("CreateAddressbook: %v", err)
	}
	backend := New(svc)
	gh := &godavcarddav.Handler{Backend: backend, Prefix: "/carddav"}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		ctx := WithUserID(r.Context(), r.Header.Get("X-User-ID"))
		switch r.Method {
		case "PROPPATCH":
			backend.HandleProppatch(w, r.WithContext(ctx))
			return
		case "REPORT":
			backend.serveReport(w, r.WithContext(ctx), gh)
			return
		case http.MethodGet:
			if backend.ServeRawObject(w, r.WithContext(ctx)) {
				return
			}
		case http.MethodPut:
			ctx = WithRawBody(ctx, raw)
		}
		gh.ServeHTTP(w, r.WithContext(ctx))
	})
	return &reviewEnv{h: h, pool: pool, svc: svc, ownerID: ownerID, sharee: db.UUIDString(sharee.ID),
		abURI: ab.Uri, abID: db.UUIDString(ab.ID), abDir: "/carddav/" + ownerID + "/card/" + ab.Uri + "/"}
}

func (e *reviewEnv) req(t *testing.T, user, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-User-ID", user)
	if method == http.MethodPut {
		r.Header.Set("Content-Type", "text/vcard; charset=utf-8")
	} else if body != "" {
		r.Header.Set("Content-Type", "application/xml; charset=utf-8")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func vcardWith(uid, extra string) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Test\r\nUID:" + uid + "\r\n" + extra + "END:VCARD\r\n"
}

func multiget(hrefs ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"><D:prop><D:getetag/><C:address-data/></D:prop>`)
	for _, h := range hrefs {
		b.WriteString("<D:href>" + h + "</D:href>")
	}
	b.WriteString(`</C:addressbook-multiget>`)
	return b.String()
}

// A card whose name needs escaping in a URL ("c 1" → c%201.vcf) must still be served raw by
// REPORT: the href in the multistatus is escaped, the stored name is not.
func TestReportServesRawCardForEscapedName(t *testing.T) {
	e := setupReview(t)
	// lower-case property names survive only in the raw bytes: go-vcard re-encodes as NOTE:
	raw := vcardWith("c 1", "note:raw-marker\r\n")
	if rec := e.req(t, e.ownerID, http.MethodPut, e.abDir+"c%201.vcf", raw, nil); rec.Code >= 300 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	rep := e.req(t, e.ownerID, "REPORT", e.abDir, multiget(e.abDir+"c%201.vcf"), map[string]string{"Depth": "1"})
	if rep.Code != http.StatusMultiStatus {
		t.Fatalf("REPORT: %d %s", rep.Code, rep.Body.String())
	}
	if !strings.Contains(rep.Body.String(), "note:raw-marker") {
		t.Fatalf("REPORT re-serialized the card instead of serving it raw:\n%s", rep.Body.String())
	}
}

// A card that contains another response's placeholder text must not swap contents with it:
// the placeholders used to be substituted one by one in random map order.
func TestReportPlaceholdersDoNotCrossTalk(t *testing.T) {
	e := setupReview(t)
	a := vcardWith("a", "NOTE:__KF_RAWVCARD_1__ from a\r\n")
	b := vcardWith("b", "NOTE:marker-b\r\n")
	for name, body := range map[string]string{"a": a, "b": b} {
		if rec := e.req(t, e.ownerID, http.MethodPut, e.abDir+name+".vcf", body, nil); rec.Code >= 300 {
			t.Fatalf("PUT %s: %d", name, rec.Code)
		}
	}
	for i := 0; i < 20; i++ {
		rep := e.req(t, e.ownerID, "REPORT", e.abDir, multiget(e.abDir+"a.vcf", e.abDir+"b.vcf"), map[string]string{"Depth": "1"})
		out := rep.Body.String()
		if strings.Count(out, "marker-b") != 1 || strings.Count(out, "__KF_RAWVCARD_1__ from a") != 1 {
			t.Fatalf("round %d: cards mixed up:\n%s", i, out)
		}
		ia, ib := strings.Index(out, "a.vcf"), strings.Index(out, "b.vcf")
		if ia < 0 || ib < 0 || strings.Index(out, "marker-b") < ib {
			t.Fatalf("round %d: b's card is not in b's response:\n%s", i, out)
		}
	}
}

// If-Match must hold at the moment of the write. With the address book row locked, two
// writers holding the same etag both used to pass the check (made before the transaction)
// and both overwrote the card; now the second one sees the new etag and gets 412.
func TestIfMatchIsCheckedAtomically(t *testing.T) {
	e := setupReview(t)
	ctx := context.Background()
	put := e.req(t, e.ownerID, http.MethodPut, e.abDir+"x.vcf", vcardWith("x", ""), nil)
	if put.Code >= 300 {
		t.Fatalf("PUT: %d", put.Code)
	}
	etag := put.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM addressbooks WHERE id = $1 FOR UPDATE", e.abID); err != nil {
		t.Fatal(err)
	}
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := vcardWith("x", "NOTE:writer "+string(rune('a'+i))+"\r\n")
			codes[i] = e.req(t, e.ownerID, http.MethodPut, e.abDir+"x.vcf", body, map[string]string{"If-Match": etag}).Code
		}(i)
	}
	time.Sleep(500 * time.Millisecond) // both writers are now waiting on the lock
	_ = tx.Rollback(ctx)
	wg.Wait()

	ok, failed := 0, 0
	for _, c := range codes {
		switch {
		case c < 300:
			ok++
		case c == http.StatusPreconditionFailed:
			failed++
		}
	}
	if ok != 1 || failed != 1 {
		t.Fatalf("two If-Match writers with the same etag: codes %v, want one success and one 412", codes)
	}
}

// A sharee's PROPPATCH (rename) is not saved: it must not be acknowledged with 200 either.
func TestProppatchByShareeIsForbidden(t *testing.T) {
	e := setupReview(t)
	if _, err := e.svc.ShareAddressbook(context.Background(), e.ownerID, e.abID, "sharee@x"); err != nil {
		t.Fatalf("share: %v", err)
	}
	body := `<?xml version="1.0" encoding="UTF-8"?><D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>Renamed</D:displayname></D:prop></D:set></D:propertyupdate>`
	rec := e.req(t, e.sharee, "PROPPATCH", e.abDir, body, nil)
	if rec.Code != http.StatusMultiStatus || !strings.Contains(rec.Body.String(), "403 Forbidden") {
		t.Fatalf("sharee PROPPATCH: %d %s, want 207 with a 403 propstat", rec.Code, rec.Body.String())
	}
	ab, _ := e.svc.GetAddressbook(context.Background(), e.abID)
	if ab.Name != "Контакты" {
		t.Fatalf("name changed to %q", ab.Name)
	}
	// the owner still can
	if rec := e.req(t, e.ownerID, "PROPPATCH", e.abDir, body, nil); !strings.Contains(rec.Body.String(), "200 OK") {
		t.Fatalf("owner PROPPATCH: %s", rec.Body.String())
	}
}

// Two cards with one vCard UID in an address book are refused with
// CARDDAV:no-uid-conflict (RFC 6352 §6.3.2.1); updating the existing card still works.
func TestSecondCardWithSameUIDIsRefused(t *testing.T) {
	e := setupReview(t)
	if rec := e.req(t, e.ownerID, http.MethodPut, e.abDir+"one.vcf", vcardWith("same-uid", ""), nil); rec.Code >= 300 {
		t.Fatalf("first card: %d %s", rec.Code, rec.Body.String())
	}
	rec := e.req(t, e.ownerID, http.MethodPut, e.abDir+"two.vcf", vcardWith("same-uid", "NOTE:dup\r\n"), nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "no-uid-conflict") {
		t.Fatalf("second card with the same UID: %d %s, want 403 no-uid-conflict", rec.Code, rec.Body.String())
	}
	if rec := e.req(t, e.ownerID, http.MethodPut, e.abDir+"one.vcf", vcardWith("same-uid", "NOTE:edited\r\n"), nil); rec.Code >= 300 {
		t.Fatalf("updating the card in place: %d %s", rec.Code, rec.Body.String())
	}
	objs, err := e.svc.ListAddressbookObjects(context.Background(), e.abID)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("address book holds %d cards, want 1", len(objs))
	}
}
