package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A filtered list no longer reports the page size as the total: the last page
// gives the exact count and earlier pages say that more follow.
func TestEbookLibraryFilteredTotalAndPaging(t *testing.T) {
	ctx := context.Background()
	q, s := buildEbookLibraryServer(t)
	tok, user, err := s.auth.Register(ctx, "ebl_page@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < ebookLibraryPageSize+2; i++ {
		seedBook(t, ctx, q, user.ID, fmt.Sprintf("Book %03d", i), "", "", "Poetry", "", false)
	}
	seedBook(t, ctx, q, user.ID, "100% Prose", "", "", "", "", false)
	listH := s.auth.Middleware(http.HandlerFunc(s.handleListEbooks))
	get := func(query string) ebookListResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		listH.ServeHTTP(rec, authedReq(http.MethodGet, "/me/ebooks/library?"+query, tok, "", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", query, rec.Code, rec.Body.String())
		}
		var resp ebookListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	p1 := get("genre=Poetry")
	if len(p1.Books) != ebookLibraryPageSize || !p1.HasMore || p1.Total <= int64(ebookLibraryPageSize) {
		t.Errorf("page 1: %d books, hasMore %v, total %d; want a full page, more, total > page", len(p1.Books), p1.HasMore, p1.Total)
	}
	p2 := get(fmt.Sprintf("genre=Poetry&start=%d", ebookLibraryPageSize))
	if len(p2.Books) != 2 || p2.HasMore || p2.Total != int64(ebookLibraryPageSize+2) {
		t.Errorf("page 2: %d books, hasMore %v, total %d; want 2, false, %d", len(p2.Books), p2.HasMore, p2.Total, ebookLibraryPageSize+2)
	}
	if all := get(""); all.Total != int64(ebookLibraryPageSize+3) || !all.HasMore {
		t.Errorf("unfiltered: total %d hasMore %v, want %d and more", all.Total, all.HasMore, ebookLibraryPageSize+3)
	}

	// An offset past int32 is clamped instead of failing the query.
	if huge := get("start=99999999999"); len(huge.Books) != 0 {
		t.Errorf("huge start returned %d books", len(huge.Books))
	}

	// LIKE wildcards in the search box are literal.
	if pct := get("q=%25"); len(pct.Books) != 1 || pct.Books[0].Title != "100% Prose" {
		t.Errorf("q=%%: %d books, want only 100%% Prose", len(pct.Books))
	}
}
