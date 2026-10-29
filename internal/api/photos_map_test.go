package api

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"discodrive/internal/db"
)

func tm(year int, month time.Month) time.Time {
	return time.Date(year, month, 10, 12, 0, 0, 0, time.UTC)
}

// mercCell is photo_map_cell written again in Go, to check the SQL against.
func mercCell(lat, lon float64, z int) (int64, int64) {
	n := math.Pow(2, float64(z+1))
	lat = math.Max(math.Min(lat, 85.0511287798), -85.0511287798)
	x := math.Min(math.Floor((lon+180)/360*n), n-1)
	r := lat * math.Pi / 180
	y := math.Floor((1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2 * n)
	y = math.Min(math.Max(y, 0), n-1)
	return int64(x), int64(y)
}

func TestPhotoMapCell(t *testing.T) {
	e := newPhotosEnv(t, false, 0)
	cell := func(lat, lon float64, z int) [2]int64 {
		t.Helper()
		var c []int64
		if err := e.pool.QueryRow(e.ctx, `SELECT photo_map_cell($1, $2, $3)`, lat, lon, z).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return [2]int64{c[0], c[1]}
	}
	if got := cell(0, 0, 0); got != [2]int64{1, 1} {
		t.Fatalf("(0,0,0) = %v", got)
	}
	x, y := mercCell(42.87, 74.59, 10)
	if got := cell(42.87, 74.59, 10); got != [2]int64{x, y} {
		t.Fatalf("Bishkek z10 = %v, want %d %d", got, x, y)
	}
	if a, b := cell(89, 10, 5), cell(85.06, 10, 5); a != b || a[1] != 0 {
		t.Fatalf("beyond the projection: %v %v", a, b)
	}
	if got := cell(-89, 10, 5); got[1] != 63 {
		t.Fatalf("south edge = %v", got)
	}
	if got := cell(10, 180, 3); got[0] != 15 {
		t.Fatalf("lon 180 = %v", got)
	}
	if got := cell(10, -180, 3); got[0] != 0 {
		t.Fatalf("lon -180 = %v", got)
	}
}

type mapPoint struct {
	lat, lon float64
	hash     string
	opts     assetOpts
	user2    bool
}

// mapFixture: two photos and a copy in Bishkek, one in Osh, one near the 180th meridian;
// a hidden, a trashed and a foreign one in Bishkek and one without a position.
func mapFixture(e *photosEnv) map[string]db.PhotoAsset {
	e.t.Helper()
	pts := map[string]mapPoint{
		"bishkek-old":  {lat: 42.87, lon: 74.59, opts: assetOpts{taken: tm(2019, 1), place: 1}},
		"bishkek-new":  {lat: 42.88, lon: 74.60, hash: strings.Repeat("a", 64), opts: assetOpts{taken: tm(2023, 5), place: 1}},
		"bishkek-copy": {lat: 42.88, lon: 74.60, hash: strings.Repeat("a", 64), opts: assetOpts{taken: tm(2023, 5), place: 1}},
		"osh":          {lat: 40.51, lon: 72.80, opts: assetOpts{taken: tm(2020, 3), place: 2}},
		"fiji":         {lat: -16.5, lon: 179.9, opts: assetOpts{taken: tm(2018, 2)}},
		"hidden":       {lat: 42.87, lon: 74.59, opts: assetOpts{hidden: true}},
		"trashed":      {lat: 42.87, lon: 74.59, opts: assetOpts{trash: true}},
		"foreign":      {lat: 42.87, lon: 74.59, user2: true},
		"nowhere":      {lat: math.NaN()},
	}
	out := map[string]db.PhotoAsset{}
	for _, name := range []string{"bishkek-old", "bishkek-new", "bishkek-copy", "osh", "fiji", "hidden", "trashed", "foreign", "nowhere"} {
		p := pts[name]
		u := e.user
		if p.user2 {
			u = e.user2
		}
		a := e.asset(u, p.opts)
		if !math.IsNaN(p.lat) {
			if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET lat = $2, lon = $3 WHERE id = $1`, a.ID, p.lat, p.lon); err != nil {
				e.t.Fatal(err)
			}
		}
		if p.hash != "" {
			if _, err := e.pool.Exec(e.ctx, `UPDATE photo_assets SET content_hash = $2 WHERE id = $1`, a.ID, p.hash); err != nil {
				e.t.Fatal(err)
			}
		}
		out[name] = a
	}
	return out
}

type clusterResp struct {
	Lat, Lon float64
	Count    int
	Cell     string
	Place    *string
	Country  *string
	Cover    struct{ ID string }
}

func TestPhotosMapClusters(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	as := mapFixture(e)

	var cs []clusterResp
	e.json(e.do(e.tok, "GET", "/me/photos/v1/map/clusters?z=6&bbox=69,39,81,44", nil), 200, &cs)
	if len(cs) != 2 {
		t.Fatalf("clusters %+v", cs)
	}
	bk, osh := cs[0], cs[1] // the bigger first
	if bk.Count != 2 || osh.Count != 1 {
		t.Fatalf("counts %d %d", bk.Count, osh.Count)
	}
	if bk.Cover.ID != db.UUIDString(as["bishkek-new"].ID) && bk.Cover.ID != db.UUIDString(as["bishkek-copy"].ID) {
		t.Fatalf("cover %s is not the newest", bk.Cover.ID)
	}
	if math.Abs(bk.Lat-42.875) > 1e-9 || math.Abs(bk.Lon-74.595) > 1e-9 {
		t.Fatalf("centre %v %v", bk.Lat, bk.Lon)
	}
	x, y := mercCell(42.87, 74.59, 6)
	if bk.Cell != fmt.Sprintf("%d.%d", x, y) {
		t.Fatalf("cell %s, want %d.%d", bk.Cell, x, y)
	}
	if bk.Place == nil || *bk.Place != "Bishkek" || bk.Country == nil || *bk.Country != "KG" {
		t.Fatalf("place %v country %v", bk.Place, bk.Country)
	}

	// Zoomed out, Bishkek and Osh share a cell: two places, one country.
	e.json(e.do(e.tok, "GET", "/me/photos/v1/map/clusters?z=1&bbox=69,39,81,44", nil), 200, &cs)
	if len(cs) != 1 || cs[0].Count != 3 || cs[0].Place != nil || cs[0].Country == nil || *cs[0].Country != "KG" {
		t.Fatalf("zoomed out %+v", cs)
	}

	// Across the 180th meridian (w > e).
	e.json(e.do(e.tok, "GET", "/me/photos/v1/map/clusters?z=4&bbox=170,-30,-170,0", nil), 200, &cs)
	if len(cs) != 1 || cs[0].Cover.ID != db.UUIDString(as["fiji"].ID) || cs[0].Place != nil || cs[0].Country != nil {
		t.Fatalf("antimeridian %+v", cs)
	}

	// The other user sees only their own.
	e.json(e.do(e.tok2, "GET", "/me/photos/v1/map/clusters?z=6&bbox=69,39,81,44", nil), 200, &cs)
	if len(cs) != 1 || cs[0].Cover.ID != db.UUIDString(as["foreign"].ID) {
		t.Fatalf("foreign %+v", cs)
	}

	for _, q := range []string{"z=21&bbox=0,0,1,1", "z=-1&bbox=0,0,1,1", "z=x&bbox=0,0,1,1", "z=3&bbox=0,0,1", "z=3&bbox=0,10,1,5", "z=3&bbox=0,-91,1,5", "z=3"} {
		e.json(e.do(e.tok, "GET", "/me/photos/v1/map/clusters?"+q, nil), 400, nil)
	}
}

func TestPhotosAreaFilter(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	as := mapFixture(e)
	bx, by := mercCell(42.87, 74.59, 6)
	ox, oy := mercCell(40.51, 72.80, 6)
	bishkek := fmt.Sprintf("6/%d.%d", bx, by)

	var buckets []struct {
		Month string
		Count int
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/buckets?area="+bishkek, nil), 200, &buckets)
	total := 0
	for _, b := range buckets {
		total += b.Count
	}
	if total != 2 || len(buckets) != 2 {
		t.Fatalf("buckets %+v", buckets)
	}
	var rows []struct{ ID string }
	e.json(e.do(e.tok, "GET", "/me/photos/v1/months/2023-05?area="+bishkek, nil), 200, &rows)
	if len(rows) != 1 || (rows[0].ID != db.UUIDString(as["bishkek-new"].ID) && rows[0].ID != db.UUIDString(as["bishkek-copy"].ID)) {
		t.Fatalf("month %+v", rows)
	}
	e.json(e.do(e.tok, "GET", "/me/photos/v1/months/2019-01?area="+bishkek, nil), 200, &rows)
	if len(rows) != 1 || rows[0].ID != db.UUIDString(as["bishkek-old"].ID) {
		t.Fatalf("old month %+v", rows)
	}

	// Two cells: Bishkek and Osh.
	e.json(e.do(e.tok, "GET", fmt.Sprintf("/me/photos/v1/buckets?area=%s,%d.%d", bishkek, ox, oy), nil), 200, &buckets)
	total = 0
	for _, b := range buckets {
		total += b.Count
	}
	if total != 3 {
		t.Fatalf("two cells %+v", buckets)
	}
	// Without the filter the feed has everything with or without a position.
	e.json(e.do(e.tok, "GET", "/me/photos/v1/buckets", nil), 200, &buckets)
	total = 0
	for _, b := range buckets {
		total += b.Count
	}
	if total != 5 {
		t.Fatalf("whole feed %+v", buckets)
	}

	many := make([]string, maxAreaCells+1)
	for i := range many {
		many[i] = fmt.Sprintf("%d.0", i)
	}
	for _, bad := range []string{"6/", "6", "21/1.1", "6/1", "6/x.1", "6/128.0", "6/-1.0", "6/" + strings.Join(many, ",")} {
		e.json(e.do(e.tok, "GET", "/me/photos/v1/buckets?area="+bad, nil), 400, nil)
		e.json(e.do(e.tok, "GET", "/me/photos/v1/months/2023-05?area="+bad, nil), 400, nil)
	}
}

func TestPhotosMapBounds(t *testing.T) {
	e := newPhotosEnv(t, true, 0)
	var b *struct{ W, S, E, N float64 }
	// No positions yet: null.
	e.json(e.do(e.tok, "GET", "/me/photos/v1/map/bounds", nil), 200, &b)
	if b != nil {
		t.Fatalf("empty bounds %+v", b)
	}
	mapFixture(e)
	e.json(e.do(e.tok, "GET", "/me/photos/v1/map/bounds", nil), 200, &b)
	// Fiji, Osh and Bishkek; the hidden, trashed and foreign ones do not widen it.
	if b == nil || b.W != 72.80 || b.E != 179.9 || b.S != -16.5 || b.N != 42.88 {
		t.Fatalf("bounds %+v", b)
	}
}

// The map functions must be inlinable (one expression, no FROM): the clusters query and
// the area filter call them for every located card, tens of thousands per map move.
func TestPhotoMapFunctionsInline(t *testing.T) {
	e := newPhotosEnv(t, false, 0)
	for _, fn := range []string{"photo_map_cell", "photo_map_key"} {
		var plan string
		q := `EXPLAIN (VERBOSE, COSTS OFF) SELECT ` + fn + `(lat, lon, 7) FROM photo_assets`
		rows, err := e.pool.Query(e.ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan += line + "\n"
		}
		rows.Close()
		if strings.Contains(plan, fn+"(") {
			t.Fatalf("%s is not inlined:\n%s", fn, plan)
		}
	}
}
