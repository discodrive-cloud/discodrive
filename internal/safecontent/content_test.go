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

func TestMediaSynonymsAndExtensionFallback(t *testing.T) {
	for raw, want := range map[string]string{
		"audio/mp3": "audio/mpeg", "audio/x-mpeg": "audio/mpeg", "AUDIO/MPEG3": "audio/mpeg",
		"audio/x-m4b": "audio/mp4", "audio/x-aac": "audio/aac", "audio/wave": "audio/wav",
	} {
		if got, ok := Media(raw); !ok || got != want {
			t.Errorf("Media(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	for _, tc := range []struct{ raw, name, want string }{
		{"application/octet-stream", "podcasts/u/e.mp3", "audio/mpeg"},
		{"audio/mp3", "podcasts/u/e.bin", "audio/mpeg"},
		{"", "e.M4B", "audio/mp4"},
		{"text/html", "e.mp3", "audio/mpeg"}, // served as audio with nosniff: inert
		{"application/octet-stream", "e.html", ""},
		{"text/html", "e.svg", ""},
	} {
		got, ok := MediaFor(tc.raw, tc.name)
		if tc.want == "" {
			if ok {
				t.Errorf("MediaFor(%q, %q) = %q, want refused", tc.raw, tc.name, got)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("MediaFor(%q, %q) = %q, %v; want %q", tc.raw, tc.name, got, ok, tc.want)
		}
	}
	for raw, want := range map[string]bool{"": true, "application/octet-stream": true, "binary/octet-stream; x=1": true, "text/html": false, "audio/mpeg": false} {
		if IsGeneric(raw) != want {
			t.Errorf("IsGeneric(%q) != %v", raw, want)
		}
	}
}
