// Package geo is an offline reverse geocoder over the GeoNames cities500 dataset: the
// nearest populated place within a radius, as its GeoNames id and country code. Names
// are asked for separately, in the reader's language. It knows nothing about the
// gallery (photos defines the interface this satisfies).
package geo

import (
	"archive/zip"
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// File names inside the data directory (also what the maps downloader writes).
const (
	CitiesZip = "cities500.zip"
	CitiesTxt = "cities500.txt"
	// AltNamesZip is GeoNames' alternate-names dump (~200 MB); BuildNames distils it into
	// Names ("geonameid<TAB>lang<TAB>name" lines) and the zip is then thrown away.
	AltNamesZip = "alternateNamesV2.zip"
	altNamesTxt = "alternateNamesV2.txt"
	Names       = "place-names.tsv"
)

// Langs are the languages place names are kept in: those of the web interface. Any
// other language gets the GeoNames name (usually the English or the local one).
var Langs = []string{"ru", "uk", "sr", "de", "fr", "es", "en"}

func langIndex(lang string) int {
	lang, _, _ = strings.Cut(strings.ToLower(lang), "-")
	for i, l := range Langs {
		if l == lang {
			return i
		}
	}
	return -1
}

// RadiusKm is how far a place may be from the point and still count.
const RadiusKm = 50.0

type city struct {
	lat, lon float32
	id       int32
	cc       [2]byte
	name     string
}

// Index is immutable after Load and safe for concurrent Lookup and Name.
type Index struct {
	cities []city
	grid   map[int32][]int32 // 1°x1° cell -> indexes into cities
	byID   map[int32]int32   // GeoNames id -> index into cities
	names  []map[int32]string
}

func cell(lat, lon float64) int32 {
	la := int32(math.Floor(lat)) + 90 // 0..180
	lo := int32(math.Floor(lon)) + 180
	lo = ((lo % 360) + 360) % 360
	return la*360 + lo
}

// Load reads cities500.zip and, when it is there, the names file from dir.
func Load(dir string) (*Index, error) {
	zr, err := zip.OpenReader(filepath.Join(dir, CitiesZip))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != CitiesTxt {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		ix, err := build(rc)
		if err != nil {
			return nil, err
		}
		ix.names = loadNames(filepath.Join(dir, Names)) // optional: without it places keep their GeoNames name
		return ix, nil
	}
	return nil, errors.New("geo: " + CitiesTxt + " not in archive")
}

// skipped are the feature codes left out of the index: sections of a city (a photo in
// central Madrid is "Madrid", not the quarter "Sol") and places that are no more.
var skipped = map[string]bool{"PPLX": true, "PPLH": true, "PPLQ": true, "PPLW": true}

// build parses the cities file. Columns kept: id (0), name (1), lat (4), lon (5),
// country code (8); everything else is dropped to keep the index small.
func build(r io.Reader) (*Index, error) {
	ix := &Index{grid: map[int32][]int32{}, byID: map[int32]int32{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 9 || len(f[8]) != 2 || skipped[f[7]] {
			continue
		}
		id, err0 := strconv.ParseInt(f[0], 10, 32)
		lat, err1 := strconv.ParseFloat(f[4], 64)
		lon, err2 := strconv.ParseFloat(f[5], 64)
		if err0 != nil || id <= 0 || err1 != nil || err2 != nil {
			continue
		}
		c := city{lat: float32(lat), lon: float32(lon), id: int32(id), name: strings.Clone(f[1])}
		copy(c.cc[:], f[8])
		ix.byID[c.id] = int32(len(ix.cities))
		ix.grid[cell(lat, lon)] = append(ix.grid[cell(lat, lon)], int32(len(ix.cities)))
		ix.cities = append(ix.cities, c)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(ix.cities) == 0 {
		return nil, errors.New("geo: no cities")
	}
	return ix, nil
}

// Len is the number of places in the index.
func (ix *Index) Len() int { return len(ix.cities) }

// Lookup returns the GeoNames id of the nearest place within RadiusKm and its ISO
// country code.
func (ix *Index) Lookup(lat, lon float64) (id int32, country string, ok bool) {
	if ix == nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 || math.IsNaN(lat) || math.IsNaN(lon) {
		return 0, "", false
	}
	// 1° of latitude is ~111 km; a degree of longitude shrinks with cos(lat).
	dLat := int(math.Ceil(RadiusKm / 111.0))
	dLon := 180
	if c := math.Cos(lat * math.Pi / 180); c > 0.01 {
		if d := int(math.Ceil(RadiusKm / (111.0 * c))); d < 180 {
			dLon = d
		}
	}
	best, bestD := int32(-1), math.MaxFloat64
	baseLa := int32(math.Floor(lat)) + 90
	baseLo := int32(math.Floor(lon)) + 180
	for la := int32(-dLat); la <= int32(dLat); la++ {
		row := baseLa + la
		if row < 0 || row > 180 {
			continue
		}
		from, to := int32(-dLon), int32(dLon)
		if dLon >= 180 {
			from, to = 0, 359 // the whole circle, once
		}
		for lo := from; lo <= to; lo++ {
			col := ((baseLo+lo)%360 + 360) % 360
			for _, i := range ix.grid[row*360+col] {
				c := &ix.cities[i]
				if d := haversineKm(lat, lon, float64(c.lat), float64(c.lon)); d < bestD {
					best, bestD = i, d
				}
			}
		}
	}
	if best < 0 || bestD > RadiusKm {
		return 0, "", false
	}
	c := &ix.cities[best]
	return c.id, string(c.cc[:]), true
}

// Name is the place's name in lang (a BCP 47 tag; only the language counts), or its
// GeoNames name where there is none in that language. "" for an unknown id.
func (ix *Index) Name(id int32, lang string) string {
	if ix == nil {
		return ""
	}
	i, ok := ix.byID[id]
	if !ok {
		return ""
	}
	if l := langIndex(lang); l >= 0 && l < len(ix.names) {
		if n := ix.names[l][id]; n != "" {
			return n
		}
	}
	return ix.cities[i].name
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := p2-p1, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// String is for logs.
func (ix *Index) String() string { return fmt.Sprintf("geo index (%d places)", len(ix.cities)) }

func loadNames(path string) []map[int32]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := make([]map[int32]string, len(Langs))
	for i := range out {
		out[i] = map[int32]string{}
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		f := strings.SplitN(sc.Text(), "\t", 3)
		if len(f) != 3 || f[2] == "" {
			continue
		}
		id, err := strconv.ParseInt(f[0], 10, 32)
		if l := langIndex(f[1]); err == nil && l >= 0 {
			out[l][int32(id)] = f[2]
		}
	}
	return out
}

// BuildNames streams the alternate-names dump once and writes out the names of every
// place of cities500 in each of Langs (historic and colloquial ones are skipped, a
// preferred one wins, otherwise the first; a name equal to the GeoNames one is left
// out). out is written through a .part file. The result is MBs where the dump is hundreds.
func BuildNames(altZip, citiesZip, out string) error {
	want, err := cityIDs(citiesZip)
	if err != nil {
		return err
	}
	zr, err := zip.OpenReader(altZip)
	if err != nil {
		return err
	}
	defer zr.Close()
	var rc io.ReadCloser
	for _, f := range zr.File {
		if f.Name == altNamesTxt {
			if rc, err = f.Open(); err != nil {
				return err
			}
			break
		}
	}
	if rc == nil {
		return errors.New("geo: " + altNamesTxt + " not in archive")
	}
	defer rc.Close()
	type cand struct {
		name string
		pref bool
	}
	type key struct {
		id   string
		lang int
	}
	best := map[key]cand{}
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		// 0 id, 1 geonameid, 2 isolanguage, 3 name, 4 isPreferred, 5 isShort, 6 isColloquial, 7 isHistoric
		if len(f) < 8 || f[6] == "1" || f[7] == "1" || f[3] == "" {
			continue
		}
		l := langIndex(f[2])
		plain, ok := want[f[1]]
		if l < 0 || f[2] != Langs[l] || !ok || f[3] == plain {
			continue
		}
		k, c := key{f[1], l}, cand{name: f[3], pref: f[4] == "1"}
		if old, ok := best[k]; !ok || (c.pref && !old.pref) {
			best[k] = cand{name: strings.Clone(c.name), pref: c.pref}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	var b strings.Builder
	for k, c := range best {
		b.WriteString(k.id + "\t" + Langs[k.lang] + "\t" + c.name + "\n")
	}
	part := out + ".part"
	if err := os.WriteFile(part, []byte(b.String()), 0o640); err != nil {
		return err
	}
	return os.Rename(part, out)
}

// cityIDs maps the GeoNames id of every place in cities500 to its plain name.
func cityIDs(citiesZip string) (map[string]string, error) {
	zr, err := zip.OpenReader(citiesZip)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != CitiesTxt {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		ids := map[string]string{}
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if f := strings.SplitN(sc.Text(), "\t", 3); len(f) >= 2 {
				ids[f[0]] = strings.Clone(f[1])
			}
		}
		return ids, sc.Err()
	}
	return nil, errors.New("geo: " + CitiesTxt + " not in archive")
}
