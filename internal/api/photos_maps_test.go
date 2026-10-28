package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discodrive/internal/photos"
)

func TestPhotoMaps_AdminAndServe(t *testing.T) {
	e := newPhotosEnv(t, false, 1)
	if _, err := e.pool.Exec(e.ctx, "UPDATE users SET role = 'admin' WHERE id = $1", e.user.ID); err != nil {
		t.Fatal(err)
	}
	// admin only
	if rec := e.do(e.tok2, "GET", "/admin/photos/v1/maps", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("user on admin route: %d", rec.Code)
	}
	if rec := e.do(e.tok2, "PUT", "/admin/photos/v1/maps", map[string]any{"region": nil}); rec.Code != http.StatusForbidden {
		t.Fatalf("user PUT: %d", rec.Code)
	}
	var st struct {
		Region, State, Error string
		Bytes                int64
	}
	e.json(e.do(e.tok, "GET", "/admin/photos/v1/maps", nil), 200, &st)
	if st.State != "unavailable" || st.Region != "" { // the test env has no pmtiles tool
		t.Fatalf("initial %+v", st)
	}
	var full struct {
		Regions []struct {
			ID          string
			ApproxBytes int64
		}
	}
	e.json(e.do(e.tok, "GET", "/admin/photos/v1/maps", nil), 200, &full)
	if len(full.Regions) != 3 || full.Regions[0].ID != "centralasia" || full.Regions[0].ApproxBytes < 1e9 {
		t.Fatalf("regions %+v", full.Regions)
	}
	if rec := e.do(e.tok, "PUT", "/admin/photos/v1/maps", map[string]any{"region": "centralasia"}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT without the tool: %d", rec.Code)
	}
	if rec := e.do(e.tok, "PUT", "/admin/photos/v1/maps", map[string]any{"region": "atlantis"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown region: %d", rec.Code)
	}
	if rec := e.do(e.tok, "PUT", "/admin/photos/v1/maps", map[string]any{"region": ""}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty region: %d", rec.Code)
	}
	e.json(e.do(e.tok, "PUT", "/admin/photos/v1/maps", map[string]any{"region": nil}), 200, &st)
	if st.State != "unavailable" {
		t.Fatalf("cleared %+v", st)
	}

	// users: not ready, then ready once the files and the ready state exist
	var ready struct{ Ready bool }
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/maps/status", nil), 200, &ready)
	if ready.Ready {
		t.Fatal("ready without data")
	}
	if rec := e.do("", "GET", "/me/photos/v1/maps/status", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	if rec := e.do(e.tok2, "GET", "/me/photos/v1/maps/region.pmtiles", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("no tiles: %d", rec.Code)
	}
	if err := os.MkdirAll(filepath.Join(e.maps.AssetsDir(), "fonts", "F"), 0o750); err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{e.maps.RegionFile(): "0123456789", filepath.Join(e.maps.AssetsDir(), "fonts", "F", "0-255.pbf"): "pbf"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{"photos.maps.region": "world", "photos.maps.state": `{"state":"ready","bytes":10}`} {
		if _, err := e.pool.Exec(e.ctx, "INSERT INTO settings (key, value) VALUES ($1, $2)", k, v); err != nil {
			t.Fatal(err)
		}
	}
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/maps/status", nil), 200, &ready)
	if !ready.Ready {
		t.Fatal("not ready")
	}
	rec := e.do(e.tok2, "GET", "/me/photos/v1/maps/region.pmtiles", nil, "Range", "bytes=0-3")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "0123" {
		t.Fatalf("range: %d %q", rec.Code, rec.Body.String())
	}
	if rec := e.do(e.tok2, "GET", "/me/photos/v1/maps/assets/fonts/F/0-255.pbf", nil); rec.Code != 200 || rec.Body.String() != "pbf" {
		t.Fatalf("asset: %d %q", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/me/photos/v1/maps/assets/../region.pmtiles", "/me/photos/v1/maps/assets/%2e%2e/region.pmtiles", "/me/photos/v1/maps/assets/fonts/..%2f..%2fregion.pmtiles"} {
		if rec := e.do(e.tok2, "GET", p, nil); rec.Code == 200 && rec.Body.String() == "0123456789" {
			t.Fatalf("traversal via %s served the tiles", p)
		}
	}
	// the admin view reports the persisted state
	e.json(e.do(e.tok, "GET", "/admin/photos/v1/maps", nil), 200, &st)
	if st.State != "ready" || st.Region != "world" || st.Bytes != 10 {
		t.Fatalf("admin state %+v", st)
	}
}

// tinyGeo knows two places: 1 (north of 40°) and 2, each named in ru and en.
type tinyGeo struct{}

func (tinyGeo) Lookup(lat, lon float64) (int32, string, bool) {
	if lat > 40 {
		return 1, "KG", true
	}
	return 0, "", false
}

func (tinyGeo) Name(id int32, lang string) string {
	names := map[int32][2]string{1: {"Бишкек", "Bishkek"}, 2: {"Ош", "Osh"}}[id]
	if strings.HasPrefix(lang, "ru") {
		return names[0]
	}
	return names[1]
}

func TestPhotoPlaces_Backfill(t *testing.T) {
	e := newPhotosEnv(t, false, 1)
	hit := e.asset(e.user, assetOpts{})
	miss := e.asset(e.user, assetOpts{})
	noGPS := e.asset(e.user, assetOpts{})
	for id, lat := range map[any]float64{hit.ID: 42.8, miss.ID: 1.0} {
		if _, err := e.pool.Exec(e.ctx, "UPDATE photo_assets SET lat = $2, lon = 74 WHERE id = $1", id, lat); err != nil {
			t.Fatal(err)
		}
	}
	n, err := photos.BackfillPlaces(e.ctx, e.q, tinyGeo{})
	if err != nil || n != 2 {
		t.Fatalf("backfill: %d %v", n, err)
	}
	place := func(id any) (p *int32, c *string) {
		if err := e.pool.QueryRow(e.ctx, "SELECT place_id, country_code FROM photo_assets WHERE id = $1", id).Scan(&p, &c); err != nil {
			t.Fatal(err)
		}
		return
	}
	if p, c := place(hit.ID); p == nil || *p != 1 || *c != "KG" {
		t.Fatalf("hit: %v %v", p, c)
	}
	if p, c := place(miss.ID); p == nil || *p != 0 || c == nil || *c != "" {
		t.Fatalf("miss should store 0: %v %v", p, c)
	}
	if p, _ := place(noGPS.ID); p != nil {
		t.Fatalf("asset without coordinates got a place: %d", *p)
	}
	if n, err := photos.BackfillPlaces(e.ctx, e.q, tinyGeo{}); err != nil || n != 0 {
		t.Fatalf("second pass: %d %v", n, err)
	}
}
