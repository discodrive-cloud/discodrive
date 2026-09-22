package music

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dhowden/tag"

	"discodrive/internal/storage"
)

// LyricLine represents one line of lyrics. Start is milliseconds from the
// track start (only meaningful for synced lyrics).
type LyricLine struct {
	Start int64 // milliseconds from track start
	Text  string
}

// lrcTimestamp matches an LRC timestamp tag: [mm:ss.xx] or [mm:ss:xx].
var lrcTimestamp = regexp.MustCompile(`\[(\d{1,2}):(\d{2})(?:[.:](\d{1,3}))?\]`)

// ParseLRC parses an LRC lyrics string and returns the lines plus whether
// the result is synced (i.e. had at least one timestamp tag).
//
//   - Empty input → (nil, false).
//   - Lines with [mm:ss.xx] timestamps → synced=true, Start in milliseconds.
//   - Plain text (no timestamps) → synced=false, Start=0 for every line.
func ParseLRC(raw string) (lines []LyricLine, synced bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}

	// Try to find any timestamp in the whole input first.
	if !lrcTimestamp.MatchString(raw) {
		// Plain-text: split by newlines, drop empties.
		for _, l := range strings.Split(raw, "\n") {
			l = strings.TrimSpace(l)
			if l != "" {
				lines = append(lines, LyricLine{Text: l})
			}
		}
		return lines, false
	}

	// Synced: parse line by line.
	for _, l := range strings.Split(raw, "\n") {
		matches := lrcTimestamp.FindAllStringSubmatch(l, -1)
		if matches == nil {
			continue // skip metadata-only lines or blank lines
		}
		// Strip ALL timestamp tags from the line to get the lyric text.
		text := strings.TrimSpace(lrcTimestamp.ReplaceAllString(l, ""))
		// Emit one LyricLine per timestamp tag found on this line.
		for _, m := range matches {
			mm, _ := strconv.ParseInt(m[1], 10, 64)
			ss, _ := strconv.ParseInt(m[2], 10, 64)

			var ms int64
			if m[3] != "" {
				frac := m[3]
				// Right-pad to 3 digits so "50" → "500" (centiseconds → ms).
				for len(frac) < 3 {
					frac += "0"
				}
				ms, _ = strconv.ParseInt(frac[:3], 10, 64)
			}

			start := mm*60000 + ss*1000 + ms
			lines = append(lines, LyricLine{Start: start, Text: text})
		}
	}
	// Sort by start time so chorus repeats scattered across the input produce
	// a correctly ordered result.
	sort.Slice(lines, func(i, j int) bool { return lines[i].Start < lines[j].Start })
	return lines, true
}

// ReadLyrics reads lyrics for the audio file at audioPath. Priority:
//  1. A sibling sidecar file with the same base name and .lrc extension.
//  2. The embedded unsynced lyrics tag in the audio file itself.
//
// Returns ("", false) when no lyrics are found. Never returns an error;
// absence of lyrics is not an error condition.
func ReadLyrics(audioPath string) (raw string, synced bool) {
	return readLyrics(audioPath, os.Open)
}

// ReadStoredLyrics applies the same boundary to both sidecar and embedded tags.
func ReadStoredLyrics(root, audioPath string) (string, bool) {
	return readLyrics(audioPath, storage.NewLocalDisk(root).OpenAbsolute)
}

func readLyrics(audioPath string, open func(string) (*os.File, error)) (raw string, synced bool) {
	// 1. Sidecar .lrc file.
	sidecar := strings.TrimSuffix(audioPath, filepath.Ext(audioPath)) + ".lrc"
	if f, err := open(sidecar); err == nil {
		data, readErr := io.ReadAll(io.LimitReader(f, 4<<20))
		f.Close()
		if readErr == nil && len(data) > 0 {
			content := string(data)
			_, s := ParseLRC(content)
			return content, s
		}
	}

	// 2. Embedded tag.
	f, err := open(audioPath)
	if err != nil {
		return "", false
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return "", false
	}
	lyr := m.Lyrics()
	if lyr == "" {
		return "", false
	}
	_, s := ParseLRC(lyr)
	return lyr, s
}
