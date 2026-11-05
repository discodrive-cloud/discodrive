package faces

import (
	"math"
	"testing"
)

// vec is a unit vector at angle deg in the plane of the first two axes: the cosine
// distance between angles a and b is 1 - cos(a-b) (10° ≈ 0.015, 90° = 1).
func vec(deg float64) []float32 {
	v := make([]float32, 512)
	r := deg * math.Pi / 180
	v[0], v[1] = float32(math.Cos(r)), float32(math.Sin(r))
	return v
}

func id(n byte) ID { return ID{n} }

var params = Params{MaxDistance: 0.5, MinFaces: 3}

type idGen struct{ n byte }

func (g *idGen) next() ID { g.n++; return ID{200 + g.n} }

func TestClusterCoresMakePerson(t *testing.T) {
	fs := []Face{{ID: id(1), Vec: vec(0), New: true}, {ID: id(2), Vec: vec(5), New: true}, {ID: id(3), Vec: vec(10), New: true}}
	g := &idGen{}
	got := Cluster(fs, params, g.next)
	if len(got) != 3 || g.n != 1 {
		t.Fatalf("assign %v, persons %d", got, g.n)
	}
	if got[id(1)] != got[id(2)] || got[id(2)] != got[id(3)] {
		t.Fatalf("split: %v", got)
	}
}

func TestClusterLoneFaceStays(t *testing.T) {
	fs := []Face{{ID: id(1), Vec: vec(0), New: true}, {ID: id(2), Vec: vec(180), New: true}}
	g := &idGen{}
	if got := Cluster(fs, params, g.next); len(got) != 0 || g.n != 0 {
		t.Fatalf("assign %v, persons %d", got, g.n)
	}
}

func TestClusterDeferredJoins(t *testing.T) {
	// 1,2,3 are a tight cluster; 4 is near 3 only (not a core: two matches with itself).
	// Coming first in order, it must still join the cluster's person.
	fs := []Face{
		{ID: id(4), Vec: vec(70), New: true},
		{ID: id(1), Vec: vec(0), New: true}, {ID: id(2), Vec: vec(5), New: true}, {ID: id(3), Vec: vec(30), New: true},
	}
	g := &idGen{}
	got := Cluster(fs, params, g.next)
	if g.n != 1 || got[id(4)] != got[id(1)] || got[id(4)] == (ID{}) {
		t.Fatalf("assign %v, persons %d", got, g.n)
	}
}

func TestClusterKeepsAssigned(t *testing.T) {
	p, q := id(100), id(101)
	fs := []Face{
		{ID: id(1), Vec: vec(0), Person: p, HasPerson: true},
		{ID: id(2), Vec: vec(5), Person: q, HasPerson: true},
		{ID: id(3), Vec: vec(10), New: true},
	}
	g := &idGen{}
	got := Cluster(fs, params, g.next)
	if _, moved := got[id(1)]; moved {
		t.Fatal("assigned face reassigned")
	}
	if _, moved := got[id(2)]; moved {
		t.Fatal("assigned face reassigned")
	}
	if got[id(3)] != q && got[id(3)] != p {
		t.Fatalf("new face not joined: %v", got)
	}
	if g.n != 0 {
		t.Fatalf("created %d persons", g.n)
	}
}

func TestClusterJoinsHiddenPerson(t *testing.T) {
	// A person the user hid is an ordinary person to the pass: new faces of a passer-by
	// join it instead of making the passer-by appear again.
	hidden := id(100)
	fs := []Face{
		{ID: id(1), Vec: vec(0), Person: hidden, HasPerson: true},
		{ID: id(2), Vec: vec(3), New: true}, {ID: id(3), Vec: vec(6), New: true}, {ID: id(4), Vec: vec(9), New: true},
	}
	g := &idGen{}
	got := Cluster(fs, params, g.next)
	if g.n != 0 || got[id(2)] != hidden || got[id(3)] != hidden || got[id(4)] != hidden {
		t.Fatalf("assign %v, persons %d", got, g.n)
	}
}

func TestClusterHiddenAssetsMakePerson(t *testing.T) {
	// Faces on hidden photos form a person too: lists show only visible photos anyway,
	// and unhiding must bring the person without a new detection.
	fs := []Face{{ID: id(1), Vec: vec(0), New: true}, {ID: id(2), Vec: vec(5), New: true}, {ID: id(3), Vec: vec(10), New: true}}
	g := &idGen{}
	if got := Cluster(fs, params, g.next); len(got) != 3 || g.n != 1 {
		t.Fatalf("assign %v, persons %d", got, g.n)
	}
}

func TestClusterOldFaceWakesNearNew(t *testing.T) {
	// 1 was alone at an earlier pass; two new faces next to it make all three a person.
	// 9, old and alone far away, is not looked at again.
	fs := []Face{
		{ID: id(1), Vec: vec(0)},
		{ID: id(9), Vec: vec(180)},
		{ID: id(2), Vec: vec(4), New: true}, {ID: id(3), Vec: vec(8), New: true},
	}
	g := &idGen{}
	got := Cluster(fs, params, g.next)
	if g.n != 1 || got[id(1)] != got[id(2)] || got[id(3)] != got[id(1)] {
		t.Fatalf("assign %v, persons %d", got, g.n)
	}
	if _, ok := got[id(9)]; ok {
		t.Fatal("far old face assigned")
	}
}

func TestClusterOldOnlyDoesNothing(t *testing.T) {
	fs := []Face{{ID: id(1), Vec: vec(0)}, {ID: id(2), Vec: vec(5)}, {ID: id(3), Vec: vec(10)}}
	g := &idGen{}
	if got := Cluster(fs, params, g.next); len(got) != 0 {
		t.Fatalf("old faces reclustered: %v", got)
	}
}

func TestNormalize(t *testing.T) {
	v := []float32{3, 4}
	Normalize(v)
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("%v", v)
	}
	z := []float32{0, 0}
	Normalize(z) // no NaN
	if z[0] != 0 || z[1] != 0 {
		t.Fatalf("%v", z)
	}
}
