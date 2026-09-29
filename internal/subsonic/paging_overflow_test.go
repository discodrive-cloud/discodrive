package subsonic

import "testing"

// Paging values past int32 used to wrap negative and surface as "database error".
// They are clamped now: huge offsets give an empty page, huge counts a bounded one.
func TestPagingParamsDoNotOverflowInt32(t *testing.T) {
	h, _ := setupSubsonic(t)
	for _, tc := range []struct{ endpoint, query string }{
		{"search3", "query=&songOffset=2147483648&artistCount=2147483648&albumOffset=99999999999999999999"},
		{"getAlbumList2", "type=newest&size=2147483648&offset=2147483648"},
		{"getAlbumList2", "type=byYear&fromYear=2147483648&toYear=1"},
		{"getRandomSongs", "size=4294967297&fromYear=99999999999"},
		{"getSongsByGenre", "genre=x&count=2147483648&offset=2147483648"},
		{"getNewestPodcasts", "count=2147483648"},
	} {
		resp := doGet(h, testAPIKey, tc.endpoint, tc.query)
		if resp == nil || resp["status"] != "ok" {
			t.Errorf("%s?%s: %v, want ok", tc.endpoint, tc.query, resp)
		}
	}
}
