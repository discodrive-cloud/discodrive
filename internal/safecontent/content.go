// Package safecontent defines the types allowed to execute no active content on our origin.
package safecontent

import (
	"mime"
	"net/http"
)

// Media normalizes a known audio/video MIME type; untrusted types are rejected.
func Media(raw string) (string, bool) {
	ct, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", false
	}
	switch ct {
	case "audio/mpeg", "audio/mp4", "audio/mp4a-latm", "audio/x-m4a", "audio/aac", "audio/flac", "audio/x-flac", "audio/ogg", "audio/opus", "audio/wav", "audio/x-wav", "audio/webm", "application/ogg", "video/mp4", "video/webm", "video/ogg", "video/quicktime", "video/x-matroska", "video/x-m4v":
		return ct, true
	}
	return "", false
}

// Raster identifies bytes, never the supplied filename or embedded MIME metadata.
func Raster(data []byte) (string, bool) {
	ct := http.DetectContentType(data)
	switch ct {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return ct, true
	}
	return "", false
}

// InlineType permits only inert media and raster images.
func InlineType(raw string) (string, bool) {
	if ct, ok := Media(raw); ok {
		return ct, true
	}
	ct, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", false
	}
	switch ct {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return ct, true
	}
	return "", false
}
