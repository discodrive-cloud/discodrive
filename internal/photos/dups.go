package photos

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/photos/media"
)

// DupCandidate is a top-level photo/video card considered for duplicate search.
type DupCandidate struct {
	ID      pgtype.UUID
	Key     string // the content: content_hash, or the id as text; dismissals name it
	Kind    string // "photo" | "video"
	TakenAt time.Time
	// TakenLocal is the wall clock at the place of capture (taken_local). Zero in
	// candidates built without it; when() then falls back to TakenAt.
	TakenLocal  time.Time
	FileDated   bool   // date_source = 'file'
	ExifDated   bool   // date_source = 'exif'
	Make, Model string // lower case, trimmed (LoadDuplicateGroupsWith does it)
	W, H        int32
	DurationMs  int64 // 0 = unknown
	Hash        uint64
	Name        string
	Bytes       int64 // total size of all byte-identical copies
	HasKids     bool  // has live-video or original children
	IndexedAt   time.Time
	DiskPath    string // node disk_path of the representative card
	Copies      int    // byte copies collapsed into this card, >= 1
}

const (
	DupTimeWindow     = 2 * time.Second
	DupMaxDistTimed   = 4
	DupMaxDistUntimed = 2
	DupDurationTol    = time.Second
)

// DupParams are the tunable thresholds of the duplicate rule.
type DupParams struct {
	TimeWindow     time.Duration
	MaxDistTimed   int
	MaxDistUntimed int
	DurationTol    time.Duration
}

var DefaultDupParams = DupParams{DupTimeWindow, DupMaxDistTimed, DupMaxDistUntimed, DupDurationTol}

// DupPair is an unordered pair of candidate keys (DupCandidate.Key), the smaller
// (byte-wise) first: what «Оставить все» records.
type DupPair [2]string

func MakeDupPair(a, b string) DupPair {
	if a <= b {
		return DupPair{a, b}
	}
	return DupPair{b, a}
}

// NormCamera is how make and model are compared: case and outer spaces ignored.
func NormCamera(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// sameCamera: make and model, each compared only when both sides know it. The values
// are normalised already (NormCamera).
func sameCamera(a, b DupCandidate) bool {
	if a.Make != "" && b.Make != "" && a.Make != b.Make {
		return false
	}
	if a.Model != "" && b.Model != "" && a.Model != b.Model {
		return false
	}
	return true
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// when is the time two shots are compared by: the camera's wall clock. TakenAt is the
// true instant only for files that record a UTC offset and the wall clock labelled UTC
// for the rest, so a RAW and the JPEG a converter made of it (offset tags dropped) are
// hours apart by TakenAt and identical by wall clock.
func (c DupCandidate) when() time.Time {
	if !c.TakenLocal.IsZero() {
		return c.TakenLocal
	}
	return c.TakenAt
}

// IsDup is the edge rule with default thresholds (dismissed pairs ignored).
func IsDup(a, b DupCandidate) bool { return IsDupWith(DefaultDupParams, a, b) }

// IsDupWith is the edge rule: same kind, close perceptual hash, close capture time when
// both dates are trustworthy (a stricter hash bound otherwise), close duration for videos,
// compatible camera. The cheap tests come first.
func IsDupWith(p DupParams, a, b DupCandidate) bool {
	if a.Kind != b.Kind {
		return false
	}
	timed := !a.FileDated && !b.FileDated
	maxDist := p.MaxDistUntimed
	if timed {
		maxDist = p.MaxDistTimed
	}
	if Hamming(a.Hash, b.Hash) > maxDist {
		return false
	}
	if timed && absDur(a.when().Sub(b.when())) > p.TimeWindow {
		return false
	}
	if a.Kind == "video" && a.DurationMs > 0 && b.DurationMs > 0 {
		d := time.Duration(a.DurationMs-b.DurationMs) * time.Millisecond
		if absDur(d) > p.DurationTol {
			return false
		}
	}
	return sameCamera(a, b)
}

var copySuffixRe = regexp.MustCompile(`(?i)( \(\d+\)| copy)$`)

func hasCopySuffix(name string) bool {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return copySuffixRe.MatchString(base)
}

// RankKeep sorts g so that the best candidate to keep comes first:
// kids, non-RAW, larger resolution, exif date, no copy suffix, larger size,
// earlier indexing, then the smaller id (so the choice is deterministic).
func RankKeep(g []DupCandidate) {
	sort.SliceStable(g, func(i, j int) bool {
		a, b := g[i], g[j]
		if a.HasKids != b.HasKids {
			return a.HasKids
		}
		if ar, br := media.IsRawName(a.Name), media.IsRawName(b.Name); ar != br {
			return !ar
		}
		if ap, bp := int64(a.W)*int64(a.H), int64(b.W)*int64(b.H); ap != bp {
			return ap > bp
		}
		if a.ExifDated != b.ExifDated {
			return a.ExifDated
		}
		if ac, bc := hasCopySuffix(a.Name), hasCopySuffix(b.Name); ac != bc {
			return !ac
		}
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		if !a.IndexedAt.Equal(b.IndexedAt) {
			return a.IndexedAt.Before(b.IndexedAt)
		}
		return string(a.ID.Bytes[:]) < string(b.ID.Bytes[:])
	})
}

// GroupDuplicates groups candidates by IsDup (default params); see GroupDuplicatesWith.
func GroupDuplicates(c []DupCandidate, dismissed map[DupPair]bool) [][]DupCandidate {
	return GroupDuplicatesWith(DefaultDupParams, c, dismissed)
}

// seedLess is the order in which cards start groups and are offered to them: capture time,
// then id, so every load of the same cards builds the same groups.
func seedLess(a, b *DupCandidate) bool {
	if !a.TakenAt.Equal(b.TakenAt) {
		return a.TakenAt.Before(b.TakenAt)
	}
	return string(a.ID.Bytes[:]) < string(b.ID.Bytes[:])
}

// GroupDuplicatesWith returns groups of size >= 2 built by complete linkage: a card joins
// a group only if it is a duplicate (IsDupWith) of every card already in it and no pair it
// would make with them is dismissed. Chains (a~b, b~c, a!~c) therefore never merge: the
// earliest card (seedLess) takes its closest-in-order partners and the rest start their own
// groups. Each group is ordered by RankKeep; groups are ordered by the first element's
// TakenAt, newest first.
func GroupDuplicatesWith(p DupParams, c []DupCandidate, dismissed map[DupPair]bool) [][]DupCandidate {
	order := make([]int, len(c))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(x, y int) bool { return seedLess(&c[order[x]], &c[order[y]]) })
	rank := make([]int, len(c)) // position in the seed order
	for r, i := range order {
		rank[i] = r
	}

	adj := dupEdges(p, c)
	used := make([]bool, len(c))
	var out [][]DupCandidate
	for _, s := range order {
		if used[s] || len(adj[s]) == 0 {
			continue
		}
		used[s] = true
		nb := adj[s]
		sort.Slice(nb, func(x, y int) bool { return rank[nb[x]] < rank[nb[y]] })
		members := []int{s}
		for _, j := range nb {
			if used[j] {
				continue
			}
			ok := true
			for _, m := range members {
				if dismissed[MakeDupPair(c[m].Key, c[j].Key)] || (m != s && !IsDupWith(p, c[m], c[j])) {
					ok = false
					break
				}
			}
			if ok {
				members = append(members, j)
				used[j] = true
			}
		}
		if len(members) < 2 {
			continue
		}
		g := make([]DupCandidate, len(members))
		for k, m := range members {
			g[k] = c[m]
		}
		RankKeep(g)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i][0].TakenAt.Equal(out[j][0].TakenAt) {
			return out[i][0].TakenAt.After(out[j][0].TakenAt)
		}
		return string(out[i][0].ID.Bytes[:]) < string(out[j][0].ID.Bytes[:])
	})
	return out
}

// dupEdges lists, for every candidate, the candidates IsDupWith pairs it with. Candidates
// are split by kind. Two trustworthy dates are compared within the time window of a sweep
// in capture order; a card dated from its file can match any card of its kind, but only
// within MaxDistUntimed bits, so those pairs come from a pigeonhole index: split into
// MaxDistUntimed+1 chunks, two hashes that close agree exactly on at least one chunk.
func dupEdges(p DupParams, c []DupCandidate) [][]int {
	adj := make([][]int, len(c))
	add := func(i, j int) {
		adj[i] = append(adj[i], j)
		adj[j] = append(adj[j], i)
	}
	byKind := map[string][]int{}
	for i := range c {
		byKind[c[i].Kind] = append(byKind[c[i].Kind], i)
	}
	for _, idx := range byKind {
		var timed, filed []int
		for _, i := range idx {
			if c[i].FileDated {
				filed = append(filed, i)
			} else {
				timed = append(timed, i)
			}
		}
		sort.Slice(timed, func(x, y int) bool { return c[timed[x]].when().Before(c[timed[y]].when()) })
		for x, i := range timed {
			for _, j := range timed[x+1:] {
				if c[j].when().Sub(c[i].when()) > p.TimeWindow {
					break
				}
				if IsDupWith(p, c[i], c[j]) {
					add(i, j)
				}
			}
		}
		if len(filed) == 0 || p.MaxDistUntimed < 0 {
			continue
		}
		isFiled := make(map[int]bool, len(filed))
		for _, i := range filed {
			isFiled[i] = true
		}
		// Each pair is tried once without remembering the pairs tried: real hashes are
		// skewed (a flat band zeroes a chunk), one bucket can hold thousands of cards, and
		// a set of the pairs seen then runs to millions of entries.
		try := func(i, j int) {
			if i == j || (isFiled[j] && j < i) { // a filed-filed pair once, from its lower index
				return
			}
			if IsDupWith(p, c[i], c[j]) {
				add(i, j)
			}
		}
		chunks := p.MaxDistUntimed + 1
		if chunks > 64 {
			for _, i := range filed {
				for _, j := range idx {
					try(i, j)
				}
			}
			continue
		}
		type slot struct {
			chunk int
			v     uint64
		}
		index := make(map[slot][]int, len(idx)*chunks)
		for _, i := range idx {
			for k := 0; k < chunks; k++ {
				s := slot{k, hashChunk(c[i].Hash, k, chunks)}
				index[s] = append(index[s], i)
			}
		}
		// A bucket of real hashes can hold thousands of cards (see above); scanning it for
		// every card dated from its file is quadratic. A pair within MaxDistUntimed bits
		// that agrees on chunk k has all its differing bits in the rest of the hash, so the
		// rest, split into MaxDistUntimed+1 parts, agrees on one of them too: a crowded
		// bucket is searched through that second index, built when first needed.
		type part struct {
			n int
			v uint64
		}
		crowded := map[slot]map[part][]int{}
		parts := func(s slot, bucket []int) map[part][]int {
			sub, ok := crowded[s]
			if !ok {
				sub = make(map[part][]int, len(bucket))
				for _, j := range bucket {
					rest, width := hashWithout(c[j].Hash, s.chunk, chunks)
					for n := 0; n < chunks; n++ {
						key := part{n, bitsChunk(rest, width, n, chunks)}
						sub[key] = append(sub[key], j)
					}
				}
				crowded[s] = sub
			}
			return sub
		}
		for _, i := range filed {
			for k := 0; k < chunks; k++ {
				s := slot{k, hashChunk(c[i].Hash, k, chunks)}
				// A pair sharing several chunks is taken from the first of them.
				earlier := func(j int) bool {
					for e := 0; e < k; e++ {
						if hashChunk(c[i].Hash, e, chunks) == hashChunk(c[j].Hash, e, chunks) {
							return true
						}
					}
					return false
				}
				bucket := index[s]
				if len(bucket) <= crowdedBucket {
					for _, j := range bucket {
						if !earlier(j) {
							try(i, j)
						}
					}
					continue
				}
				sub := parts(s, bucket)
				rest, width := hashWithout(c[i].Hash, k, chunks)
				for n := 0; n < chunks; n++ {
				candidates:
					for _, j := range sub[part{n, bitsChunk(rest, width, n, chunks)}] {
						if earlier(j) {
							continue
						}
						// ...and, within the bucket, from the first part they share.
						other, _ := hashWithout(c[j].Hash, k, chunks)
						for m := 0; m < n; m++ {
							if bitsChunk(rest, width, m, chunks) == bitsChunk(other, width, m, chunks) {
								continue candidates
							}
						}
						try(i, j)
					}
				}
			}
		}
	}
	return adj
}

// crowdedBucket is the bucket size from which the second index is used.
const crowdedBucket = 64

// hashChunk is chunk k of n roughly equal runs of bits of h.
func hashChunk(h uint64, k, n int) uint64 {
	return bitsChunk(h, 64, k, n)
}

// bitsChunk is chunk k of n roughly equal runs of the low width bits of h.
func bitsChunk(h uint64, width, k, n int) uint64 {
	lo, hi := k*width/n, (k+1)*width/n
	w := uint(hi - lo)
	if w >= 64 {
		return h
	}
	return (h >> uint(lo)) & (uint64(1)<<w - 1)
}

// hashWithout is h with chunk k of n cut out and the bits above it moved down, and the
// number of bits left.
func hashWithout(h uint64, k, n int) (rest uint64, width int) {
	lo, hi := k*64/n, (k+1)*64/n
	if hi-lo >= 64 {
		return 0, 0
	}
	low := h & (uint64(1)<<uint(lo) - 1)
	var high uint64
	if hi < 64 {
		high = (h >> uint(hi)) << uint(lo)
	}
	return low | high, 64 - (hi - lo)
}
