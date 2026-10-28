// Package media locates and wraps the external binaries (exiftool, vips,
// ffmpeg, ffprobe) the photo gallery relies on. The server stays CGO-free.
package media

import (
	"os"
	"os/exec"
)

// Tools holds resolved paths of the external media binaries; an empty field
// means the tool was not found.
type Tools struct {
	Exiftool string
	Vips     string
	Ffmpeg   string
	Ffprobe  string
	// Pmtiles is the go-pmtiles CLI for the gallery's map download. Optional: it is not
	// part of Available(); without it maps are reported as unavailable.
	Pmtiles string
}

// Detect resolves each tool from its PHOTOS_* env override or from PATH.
// An override that does not resolve counts as missing (no PATH fallback).
func Detect() Tools {
	return Tools{
		Exiftool: find("PHOTOS_EXIFTOOL", "exiftool"),
		Vips:     find("PHOTOS_VIPS", "vips"),
		Ffmpeg:   find("PHOTOS_FFMPEG", "ffmpeg"),
		Ffprobe:  find("PHOTOS_FFPROBE", "ffprobe"),
		Pmtiles:  find("PHOTOS_PMTILES", "pmtiles"),
	}
}

// Available reports whether all four tools were found.
func (t Tools) Available() bool {
	return t.Exiftool != "" && t.Vips != "" && t.Ffmpeg != "" && t.Ffprobe != ""
}

func find(envKey, name string) string {
	if v := os.Getenv(envKey); v != "" {
		name = v
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}
