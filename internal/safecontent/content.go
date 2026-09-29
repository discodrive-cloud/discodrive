// Package safecontent defines the types allowed to execute no active content on our origin.
package safecontent

import (
	"mime"
	"net/http"
	"path"
	"strings"
)

// mediaSynonyms maps non-standard audio types that podcast hosts send to the
// allowlisted name for the same format.
var mediaSynonyms = map[string]string{
	"audio/mp3":      "audio/mpeg",
	"audio/x-mp3":    "audio/mpeg",
	"audio/mpeg3":    "audio/mpeg",
	"audio/x-mpeg":   "audio/mpeg",
	"audio/x-mpeg-3": "audio/mpeg",
	"audio/mpg":      "audio/mpeg",
	"audio/x-mpg":    "audio/mpeg",
	"audio/m4a":      "audio/mp4",
	"audio/m4b":      "audio/mp4",
	"audio/x-m4b":    "audio/mp4",
	"audio/x-mp4":    "audio/mp4",
	"audio/x-aac":    "audio/aac",
	"audio/aacp":     "audio/aac",
	"audio/vorbis":   "audio/ogg",
	"audio/x-ogg":    "audio/ogg",
	"audio/wave":     "audio/wav",
	"audio/vnd.wave": "audio/wav",
}

// mediaExt maps file extensions to allowlisted media types.
var mediaExt = map[string]string{
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".m4b":  "audio/mp4",
	".aac":  "audio/aac",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".opus": "audio/opus",
	".flac": "audio/flac",
	".wav":  "audio/wav",
	".mp4":  "video/mp4",
	".m4v":  "video/x-m4v",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".mkv":  "video/x-matroska",
}

// MediaByExt returns the media type for name's extension (a file name, path or
// URL path) when it is a known audio/video format.
func MediaByExt(name string) (string, bool) {
	ct, ok := mediaExt[strings.ToLower(path.Ext(name))]
	return ct, ok
}

// MediaFor is the type to serve a stored media file with: the recorded type when
// it is allowlisted (after synonyms), otherwise the one its extension implies.
// The result is always an inert media type, so falling back on the extension is
// safe even when the recorded type was something else entirely.
func MediaFor(raw, name string) (string, bool) {
	if ct, ok := Media(raw); ok {
		return ct, true
	}
	return MediaByExt(name)
}

// IsGeneric reports whether raw says nothing about the format (missing, or one
// of the "just bytes" types servers use for downloads).
func IsGeneric(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	ct, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	switch ct {
	case "application/octet-stream", "binary/octet-stream", "application/binary",
		"application/x-octet-stream", "application/download", "application/force-download":
		return true
	}
	return false
}

// Media normalizes a known audio/video MIME type; untrusted types are rejected.
func Media(raw string) (string, bool) {
	ct, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", false
	}
	if canon, ok := mediaSynonyms[ct]; ok {
		return canon, true
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
