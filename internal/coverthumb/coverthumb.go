// Package coverthumb keeps downscaled cover art on disk. A grid of a hundred albums asks
// for a hundred covers at 200–300 px; without the cache each request parsed an audio
// file's tags and sent the embedded picture at full size.
package coverthumb

import (
	"bytes"
	"io"
	"path"
	"strconv"

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

// Get returns the JPEG thumbnail of the picture named by key at one of the kept sides,
// making and caching it on first use. key names the picture's content (the content hash
// of the file it comes from), so a changed file gets a new thumbnail; it must be
// lower-case hex. original supplies the full picture and is called only on a miss.
// ok is false when there is no thumbnail to give (no picture, not a decodable raster,
// too large to decode safely): the caller then serves what it served before.
func Get(root, key string, side int, original func() ([]byte, bool)) (thumb []byte, ok bool) {
	if !validKey(key) || Side(side) != side || side == 0 {
		return nil, false
	}
	disk := storage.NewLocalDisk(root)
	rel := path.Join(".covers", "thumbs", key[:2], key+"-"+strconv.Itoa(side)+".jpg")
	if f, err := disk.Open(rel); err == nil {
		data, rerr := io.ReadAll(io.LimitReader(f, maxCached))
		_ = f.Close()
		if rerr == nil && len(data) > 0 {
			return data, true
		}
	}
	data, has := original()
	if !has {
		return nil, false
	}
	thumb, err := links.MakeThumbSide(data, side)
	if err != nil {
		return nil, false
	}
	_, _, _ = disk.WriteFile(rel, bytes.NewReader(thumb)) // best-effort: the next request tries again
	return thumb, true
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
