package geo

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeData builds a three-city dataset: Bishkek, Moscow and a Kazakh-lettered town.
func writeData(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	row := func(id, name, alt, lat, lon, cc string) string {
		code := "PPL"
		if id == "5" {
			code = "PPLX"
		}
		return strings.Join([]string{id, name, name, alt, lat, lon, "P", code, cc, "", "01", "", "", "", "1000", "", "", "tz", "2020-01-01"}, "\t")
	}
	cities := strings.Join([]string{
		row("1", "Bishkek", "Bishkek,Бішкек", "42.87", "74.59", "KG"),
		row("2", "Moscow", "Moskva,Маскав", "55.7558", "37.6176", "RU"),
		row("3", "Tokyo", "Tōkyō", "35.6895", "139.69171", "JP"),
		row("4", "Anadyr", "Анадырь", "64.73", "179.9", "RU"),
		row("5", "Arbat", "Арбат", "55.75", "37.59", "RU"), // a section of Moscow: not a place of its own
	}, "\n") + "\n"
	f, err := os.Create(filepath.Join(dir, CitiesZip))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create(CitiesTxt)
	_, _ = w.Write([]byte(cities))
	_ = zw.Close()
	_ = f.Close()
	// alternateNamesV2: id, geonameid, lang, name, preferred, short, colloquial, historic
	alt := func(geo, lang, name, pref, coll, hist string) string {
		return strings.Join([]string{"9", geo, lang, name, pref, "", coll, hist}, "\t")
	}
	altRows := strings.Join([]string{
		alt("2", "be", "Маскав", "1", "", ""),     // another language: ignored
		alt("2", "ru", "Москоу", "", "", "1"),     // historic: ignored
		alt("2", "ru", "Моск", "", "1", ""),       // colloquial: ignored
		alt("2", "ru", "Москва-река", "", "", ""), // not preferred
		alt("2", "ru", "Москва", "1", "", ""),     // preferred wins
		alt("1", "ru", "Бишкек", "", "", ""),
		alt("4", "ru", "Анадырь", "", "", ""),
		alt("77", "ru", "Не наш", "1", "", ""), // not a city of the dataset
		alt("1", "uk", "Бішкек", "", "", ""),
		alt("1", "de", "Bischkek", "", "", ""),
		alt("1", "en", "Bishkek", "", "", ""),     // the plain name: not stored
		alt("1", "sr-Latn", "Biškek", "", "", ""), // not the interface's Serbian
		alt("2", "uk", "Москва", "", "", ""),
	}, "\n") + "\n"
	af, err := os.Create(filepath.Join(dir, AltNamesZip))
	if err != nil {
		t.Fatal(err)
	}
	aw := zip.NewWriter(af)
	w2, _ := aw.Create("alternateNamesV2.txt")
	_, _ = w2.Write([]byte(altRows))
	_ = aw.Close()
	_ = af.Close()
	if err := BuildNames(filepath.Join(dir, AltNamesZip), filepath.Join(dir, CitiesZip), filepath.Join(dir, Names)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGeo_LookupNearest(t *testing.T) {
	ix, err := Load(writeData(t))
	if err != nil {
		t.Fatal(err)
	}
	if ix.Len() != 4 {
		t.Fatalf("len %d", ix.Len())
	}
	cases := []struct {
		lat, lon float64
		id       int32
		country  string
	}{
		{42.9, 74.6, 1, "KG"},
		{55.8, 37.5, 2, "RU"},   // across a cell boundary
		{55.75, 37.59, 2, "RU"}, // at Arbat, a section: the city itself
		{35.7, 139.7, 3, "JP"},
		{64.7, -179.9, 4, "RU"}, // across the antimeridian (179.9E)
	}
	for _, c := range cases {
		id, co, ok := ix.Lookup(c.lat, c.lon)
		if !ok || id != c.id || co != c.country {
			t.Errorf("Lookup(%v,%v) = %d,%q,%v want %d,%q", c.lat, c.lon, id, co, ok, c.id, c.country)
		}
	}
}

func TestGeo_NamePerLanguage(t *testing.T) {
	ix, err := Load(writeData(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id         int32
		lang, want string
	}{
		{1, "ru", "Бишкек"},
		{1, "ru-RU", "Бишкек"}, // only the language counts
		{1, "uk", "Бішкек"},
		{1, "de", "Bischkek"},
		{1, "en", "Bishkek"},
		{1, "sr", "Bishkek"}, // only a Latin-script Serbian row: GeoNames name
		{1, "", "Bishkek"},
		{1, "ja", "Bishkek"}, // a language not kept
		{2, "ru", "Москва"},  // the preferred one, not historic or colloquial
		{2, "uk", "Москва"},
		{2, "fr", "Moscow"},
		{3, "ru", "Tokyo"},
		{77, "ru", ""}, // not a place of the index
	}
	for _, c := range cases {
		if got := ix.Name(c.id, c.lang); got != c.want {
			t.Errorf("Name(%d, %q) = %q, want %q", c.id, c.lang, got, c.want)
		}
	}
}

func TestGeo_OutOfRadius(t *testing.T) {
	ix, err := Load(writeData(t))
	if err != nil {
		t.Fatal(err)
	}
	// ~110 km north of Moscow, and the middle of the ocean.
	for _, pt := range [][2]float64{{56.75, 37.6}, {0, -140}, {91, 0}} {
		if id, _, ok := ix.Lookup(pt[0], pt[1]); ok {
			t.Errorf("Lookup(%v) found %d", pt, id)
		}
	}
	var nilIx *Index
	if _, _, ok := nilIx.Lookup(1, 1); ok {
		t.Error("nil index found something")
	}
}

// TestGeo_RealData loads a real GeoNames download (PHOTOS_GEO_REAL=<dir>) and prints
// the heap it costs.
func TestGeo_RealData(t *testing.T) {
	dir := os.Getenv("PHOTOS_GEO_REAL")
	if dir == "" {
		t.Skip("set PHOTOS_GEO_REAL=<dir with cities500.zip and place-names.tsv>")
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	ix, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("%d places, load %v, heap +%.1f MB", ix.Len(), took, float64(after.HeapAlloc-before.HeapAlloc)/1e6)
	for _, p := range [][2]float64{{42.8746, 74.6122}, {55.7558, 37.6173}, {48.8584, 2.2945}, {43.2, 76.9}} {
		id, c, ok := ix.Lookup(p[0], p[1])
		t.Logf("%v -> %d %q %q %q %q, %v", p, id, ix.Name(id, "ru"), ix.Name(id, "en"), ix.Name(id, "sr"), c, ok)
	}
	runtime.KeepAlive(ix)
}

func TestGeo_NoNamesKeepsPlainName(t *testing.T) {
	dir := writeData(t)
	if err := os.Remove(filepath.Join(dir, Names)); err != nil {
		t.Fatal(err)
	}
	ix, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := ix.Name(1, "ru"); got != "Bishkek" {
		t.Fatalf("got %q", got)
	}
}
