// Package coverthumb keeps downscaled cover art on disk. A grid of a hundred albums asks
// for a hundred covers at 200–300 px; without the cache each request parsed an audio
// file's tags and sent the embedded picture at full size.
package coverthumb

import (
	"bytes"
	"image"
	"io"
	"path"
	"strconv"
	"sync"
	"sync/atomic"

	"discodrive/internal/links"
	"discodrive/internal/storage"
)

// sides are the sizes kept. A request is served from the smallest one that covers it, so
// a client asking for arbitrary sizes cannot fill the disk with variants.
var sides = []int{160, 320, 640, 1200}

// Side maps the size a client asked for to the size served: the smallest kept size that
// is not below it, or 0 — the original — when none was asked for or it exceeds them all.
func Side(requested int) int {
	if requested <= 0 {
		return 0
	}
	for _, s := range sides {
		if requested <= s {
			return s
		}
	}
	return 0
}

// maxCached bounds what is read back from the cache; a thumbnail is far below it.
const maxCached = 4 << 20

// maxPixels bounds the covers that are scaled. Scaling decodes the whole picture: a
// baseline JPEG costs about 3 bytes a pixel decoded, a progressive one or a 16-bit PNG up
// to 12, so 4 Mpix is 12–48 MB for the moment of the decode. Larger covers are served as
// the file they are — what the server did before it kept thumbnails — so a machine with
// little memory never pays more for a cover than it used to.
const maxPixels = 4_000_000

// One thumbnail is made at a time, and the original is read only by the one making it:
// a cold grid of a hundred albums holds one picture in memory, not a hundred. maxWaiting
// bounds the line for that slot; a request past it gets no thumbnail and its caller
// serves the original at once rather than after the whole grid.
const maxWaiting = 16

var (
	making  sync.Mutex
	waiting atomic.Int32
)

// Get returns the JPEG thumbnail of the picture named by key at one of the kept sides,
// making and caching it on first use. key names the picture's content (the content hash
// of the file it comes from), so a changed file gets a new thumbnail; it must be
// lower-case hex. original supplies the full picture and is called only on a miss.
// ok is false when there is no thumbnail to give (no picture, not a decodable raster,
// too large to decode on a small machine, too many requests
// already waiting): the caller then serves what it served before.
func Get(root, key string, side int, original func() ([]byte, bool)) (thumb []byte, ok bool) {
	if !validKey(key) || Side(side) != side || side == 0 {
		return nil, false
	}
	disk := storage.NewLocalDisk(root)
	rel := path.Join(".covers", "thumbs", key[:2], key+"-"+strconv.Itoa(side)+".jpg")
	if thumb, ok := cached(disk, rel); ok {
		return thumb, true
	}
	if waiting.Add(1) > maxWaiting {
		waiting.Add(-1)
		return nil, false
	}
	making.Lock()
	waiting.Add(-1)
	defer making.Unlock()
	// Whoever held the slot before may have made this very thumbnail.
	if thumb, ok := cached(disk, rel); ok {
		return thumb, true
	}
	data, has := original()
	if !has {
		return nil, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, false
	}
	thumb, err = links.MakeThumbSide(data, side)
	if err != nil {
		return nil, false
	}
	_, _, _ = disk.WriteFile(rel, bytes.NewReader(thumb)) // best-effort: the next request tries again
	return thumb, true
}

func cached(disk *storage.LocalDisk, rel string) ([]byte, bool) {
	f, err := disk.Open(rel)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCached))
	return data, err == nil && len(data) > 0
}

func validKey(key string) bool {
	if len(key) < 16 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
