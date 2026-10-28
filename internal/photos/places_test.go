package photos

import "testing"

type fixedGeo struct{}

func (fixedGeo) Lookup(lat, lon float64) (int32, string, bool) { return 7, "Y", true }
func (fixedGeo) Name(id int32, lang string) string             { return lang + "-X" }

func TestSwappableGeocoder(t *testing.T) {
	var s SwappableGeocoder
	if s.Ready() {
		t.Fatal("ready when empty")
	}
	if _, _, ok := s.Lookup(1, 1); ok {
		t.Fatal("empty geocoder found a place")
	}
	if s.Name(7, "ru") != "" {
		t.Fatal("empty geocoder named a place")
	}
	s.Swap(fixedGeo{})
	if p, c, ok := s.Lookup(1, 1); !ok || p != 7 || c != "Y" || !s.Ready() || s.Name(p, "ru") != "ru-X" {
		t.Fatalf("swapped: %d %q %v", p, c, ok)
	}
	s.Swap(nil)
	if s.Ready() {
		t.Fatal("still ready after Swap(nil)")
	}
	ix := &Indexer{geo: &s}
	if ix.geoReady() {
		t.Fatal("indexer treats an empty swappable geocoder as present")
	}
	s.Swap(fixedGeo{})
	if !ix.geoReady() {
		t.Fatal("indexer ignores a loaded geocoder")
	}
}
