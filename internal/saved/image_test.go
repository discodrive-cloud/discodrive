package saved

import "testing"

func TestSniffRasterOnly(t *testing.T) {
	for name, c := range map[string]struct {
		head, want string
	}{
		"png":      {"\x89PNG\r\n\x1a\n0000", "image/png"},
		"jpeg":     {"\xff\xd8\xff\xe0", "image/jpeg"},
		"gif":      {"GIF89a....", "image/gif"},
		"webp":     {"RIFF\x00\x00\x00\x00WEBPVP8 ", "image/webp"},
		"avif":     {"\x00\x00\x00\x1cftypavif\x00\x00\x00\x00", "image/avif"},
		"svg":      {`<svg xmlns="http://www.w3.org/2000/svg"/>`, ""},
		"html":     {"<!doctype html><script>", ""},
		"mp4 ftyp": {"\x00\x00\x00\x1cftypisom", ""},
		"empty":    {"", ""},
	} {
		if got := sniffRaster([]byte(c.head)); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}
