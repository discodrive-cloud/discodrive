package safecontent

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestRasterRejectsDocuments(t *testing.T) {
	for _, data := range []string{"<!doctype html><script>alert(1)</script>", "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>", "alert(1)", ""} {
		if ct, ok := Raster([]byte(data)); ok {
			t.Fatalf("active content accepted: %s", ct)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if ct, ok := Raster(b.Bytes()); !ok || ct != "image/png" {
		t.Fatalf("PNG refused: %s", ct)
	}
}

func TestInlineMIMEPolicy(t *testing.T) {
	for _, raw := range []string{"text/html", "text/javascript", "application/javascript", "image/svg+xml", "application/xhtml+xml", "", "audio/unknown"} {
		if ct, ok := InlineType(raw); ok {
			t.Fatalf("unsafe type allowed: %s", ct)
		}
	}
	for _, raw := range []string{"audio/mpeg", "audio/mp4; codecs=mp4a.40.2", "video/mp4", "image/png", "image/jpeg"} {
		if _, ok := InlineType(raw); !ok {
			t.Fatalf("safe type rejected: %s", raw)
		}
	}
}
