// Package faces groups face embeddings into persons. It follows the mechanics Immich uses
// (incremental assignment by nearest neighbours within a cosine radius, "core" faces
// with enough neighbours create persons, the rest wait for a person to form near them);
// the code is our own.
package faces

import (
	"math"
	"runtime"
	"sort"
	"sync"
)

// ID is a face or person id (the bytes of a UUID).
type ID [16]byte

// Face is one face of a pass. Vec is normalised. New: not seen by a pass yet. Faces on
// hidden or trashed photos count like any other: what is shown is filtered on read.
type Face struct {
	ID, Person ID
	HasPerson  bool
	New        bool
	Vec        []float32
}

// Params are the thresholds: MaxDistance is the cosine distance (1 - dot) within which two
// faces are neighbours, MinFaces the neighbours (the face itself included) a core needs.
type Params struct {
	MaxDistance float32
	MinFaces    int
}

// listLen is how many nearest neighbours a pass keeps per candidate. A person is looked
// for among them before falling back to a full scan.
const listLen = 32

type neighbour struct {
	idx  int
	dist float32
}

// Cluster assigns persons to the faces without one that are new, or are near a new face
// (a lone face of an earlier pass wakes up when new faces arrive next to it). Faces that
// have a person keep it. newPerson creates a person and returns its id. The result maps
// face ids to the person each got.
func Cluster(faces []Face, p Params, newPerson func() ID) map[ID]ID {
	assign := map[ID]ID{}
	if len(faces) == 0 || p.MinFaces < 1 {
		return assign
	}
	person := make([]ID, len(faces))
	has := make([]bool, len(faces))
	for i, f := range faces {
		person[i], has[i] = f.Person, f.HasPerson
	}

	var newIdx []int
	for i, f := range faces {
		if f.New {
			newIdx = append(newIdx, i)
		}
	}
	lists := neighbourLists(faces, newIdx, p.MaxDistance)

	// Candidates: new faces without a person, then old ones without a person that a new
	// face has among its neighbours.
	cand := map[int][]neighbour{}
	var order []int
	for k, i := range newIdx {
		if !has[i] {
			cand[i] = lists[k]
			order = append(order, i)
		}
	}
	var woken []int
	for _, l := range lists {
		for _, n := range l {
			if _, ok := cand[n.idx]; !ok && !has[n.idx] && !faces[n.idx].New {
				cand[n.idx] = nil
				woken = append(woken, n.idx)
			}
		}
	}
	if len(woken) > 0 {
		wl := neighbourLists(faces, woken, p.MaxDistance)
		for k, i := range woken {
			cand[i] = wl[k]
			order = append(order, i)
		}
	}

	// Cores first; the rest are deferred to a second round so persons can form near them.
	var deferred []int
	for round := 0; round < 2; round++ {
		todo := order
		if round == 1 {
			todo = deferred
		}
		for _, i := range todo {
			if has[i] {
				continue
			}
			l := cand[i] // sorted, includes i itself
			if p.MinFaces > 1 && len(l) <= 1 {
				continue // alone
			}
			core := len(l) >= p.MinFaces
			if round == 0 && !core {
				deferred = append(deferred, i)
				continue
			}
			pid, ok := personAmong(l[:min(len(l), p.MinFaces)], i, person, has)
			if !ok {
				pid, ok = personAmong(l, i, person, has)
			}
			if !ok && len(l) == listLen {
				pid, ok = nearestWithPerson(faces, i, p.MaxDistance, person, has)
			}
			if !ok {
				if !core {
					continue
				}
				pid, ok = newPerson(), true
			}
			person[i], has[i] = pid, true
			assign[faces[i].ID] = pid
		}
	}
	return assign
}

func personAmong(l []neighbour, self int, person []ID, has []bool) (ID, bool) {
	for _, n := range l {
		if n.idx != self && has[n.idx] {
			return person[n.idx], true
		}
	}
	return ID{}, false
}

func nearestWithPerson(faces []Face, i int, maxD float32, person []ID, has []bool) (ID, bool) {
	best, bestD := -1, maxD
	for j := range faces {
		if j == i || !has[j] {
			continue
		}
		if d := 1 - dot(faces[i].Vec, faces[j].Vec); d <= bestD {
			best, bestD = j, d
		}
	}
	if best < 0 {
		return ID{}, false
	}
	return person[best], true
}

// neighbourLists returns, for each face in idx, up to listLen nearest faces within maxD
// (itself included), nearest first. The scans run in parallel on half the CPUs: a first
// pass over a big library compares every face with every other.
func neighbourLists(faces []Face, idx []int, maxD float32) [][]neighbour {
	out := make([][]neighbour, len(idx))
	workers := max(1, runtime.NumCPU()/2)
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range next {
				out[k] = nearest(faces, idx[k], maxD)
			}
		}()
	}
	for k := range idx {
		next <- k
	}
	close(next)
	wg.Wait()
	return out
}

func nearest(faces []Face, i int, maxD float32) []neighbour {
	var l []neighbour
	v := faces[i].Vec
	for j := range faces {
		d := 1 - dot(v, faces[j].Vec)
		if j == i {
			d = -1 // itself always first
		}
		if d > maxD {
			continue
		}
		if len(l) == listLen {
			if d >= l[listLen-1].dist {
				continue
			}
			l = l[:listLen-1]
		}
		at := sort.Search(len(l), func(k int) bool { return l[k].dist > d })
		l = append(l, neighbour{})
		copy(l[at+1:], l[at:])
		l[at] = neighbour{idx: j, dist: d}
	}
	return l
}

func dot(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var s0, s1, s2, s3 float32
	n := len(a) &^ 3
	for k := 0; k < n; k += 4 {
		s0 += a[k] * b[k]
		s1 += a[k+1] * b[k+1]
		s2 += a[k+2] * b[k+2]
		s3 += a[k+3] * b[k+3]
	}
	for k := n; k < len(a); k++ {
		s0 += a[k] * b[k]
	}
	return s0 + s1 + s2 + s3
}

// Normalize scales v to unit length in place (a zero vector stays zero).
func Normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(s))
	for k := range v {
		v[k] *= inv
	}
}
