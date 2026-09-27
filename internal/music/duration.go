package music

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"os"

	"github.com/mewkiz/flac"
	"github.com/tcolgate/mp3"
)

// ProbeAudio returns the duration (seconds) and average bitrate (kbps) of an
// audio file, or (0, 0) if it cannot be determined. Pure Go; dispatches by
// lowercase suffix ("mp3", "flac", "ogg"). Other formats return (0, 0) for now.
func ProbeAudio(path, suffix string) (durationSec int, bitrateKbps int) {
	switch suffix {
	case "mp3":
		return probeMP3(path)
	case "flac":
		return probeFLAC(path)
	case "ogg":
		return probeOgg(path)
	default:
		return 0, 0
	}
}

// probeMP3 iterates all MP3 frames to sum their durations, then derives average
// bitrate from file size. Handles both CBR and VBR correctly.
func probeMP3(path string) (durationSec int, bitrateKbps int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	// Buffered: the decoder reads every frame in small pieces, and on a bare *os.File
	// each of them is a syscall — that dominated the first scan of a library.
	d := mp3.NewDecoder(bufio.NewReaderSize(f, 64<<10))
	var totalNs float64
	var frame mp3.Frame
	var skipped int
	for {
		err := d.Decode(&frame, &skipped)
		if err == io.EOF {
			break
		}
		if err != nil {
			// Non-fatal frame error: skip and continue.
			continue
		}
		totalNs += frame.Duration().Seconds()
	}

	if totalNs <= 0 {
		return 0, 0
	}

	durationSec = int(math.Round(totalNs))
	if durationSec == 0 {
		durationSec = 1
	}

	if fi, err := os.Stat(path); err == nil {
		bitrateKbps = int(fi.Size() * 8 / int64(durationSec) / 1000)
	}
	return durationSec, bitrateKbps
}

// probeOgg derives duration from an Ogg container (Vorbis or Opus stream)
// without decoding audio: the sample rate comes from the codec ID header on
// the first page, the total sample count from the granule position of the
// last page. Opus granules always tick at 48 kHz and are offset by pre-skip.
func probeOgg(path string) (durationSec int, bitrateKbps int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	// First page: "OggS" capture pattern, then the codec identification packet.
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if len(head) < 28 || string(head[0:4]) != "OggS" || head[4] != 0 {
		return 0, 0
	}
	nsegs := int(head[26])
	payload := head[min(27+nsegs, len(head)):]

	var rate, preSkip int64
	switch {
	case bytes.HasPrefix(payload, []byte("\x01vorbis")) && len(payload) >= 16:
		rate = int64(binary.LittleEndian.Uint32(payload[12:16]))
	case bytes.HasPrefix(payload, []byte("OpusHead")) && len(payload) >= 12:
		preSkip = int64(binary.LittleEndian.Uint16(payload[10:12]))
		rate = 48000
	default:
		return 0, 0
	}
	if rate <= 0 {
		return 0, 0
	}

	fi, err := f.Stat()
	if err != nil {
		return 0, 0
	}
	size := fi.Size()

	// Last page: scan the file tail for the final "OggS" header and take its
	// granule position (total samples). -1 granules ("no packet ends here")
	// are skipped by the g >= 0 check.
	const tailSize = 64 * 1024
	off := max(size-tailSize, 0)
	tail := make([]byte, size-off)
	if _, err := f.ReadAt(tail, off); err != nil && err != io.EOF {
		return 0, 0
	}
	granule := int64(-1)
	for i := 0; ; {
		j := bytes.Index(tail[i:], []byte("OggS"))
		if j < 0 {
			break
		}
		p := i + j
		if p+27 <= len(tail) && tail[p+4] == 0 {
			if g := int64(binary.LittleEndian.Uint64(tail[p+6 : p+14])); g >= 0 {
				granule = g
			}
		}
		i = p + 4
	}

	samples := granule - preSkip
	if samples <= 0 {
		return 0, 0
	}
	durationSec = int(math.Round(float64(samples) / float64(rate)))
	if durationSec == 0 {
		durationSec = 1
	}
	bitrateKbps = int(size * 8 / int64(durationSec) / 1000)
	return durationSec, bitrateKbps
}

// probeFLAC reads the FLAC StreamInfo header to derive duration without
// decoding audio frames.
func probeFLAC(path string) (durationSec int, bitrateKbps int) {
	stream, err := flac.ParseFile(path)
	if err != nil {
		return 0, 0
	}
	defer stream.Close()

	info := stream.Info
	if info == nil || info.SampleRate == 0 {
		return 0, 0
	}

	durationSec = int(info.NSamples / uint64(info.SampleRate))
	if durationSec == 0 {
		return 0, 0
	}

	if fi, err := os.Stat(path); err == nil {
		bitrateKbps = int(fi.Size() * 8 / int64(durationSec) / 1000)
	}
	return durationSec, bitrateKbps
}
