package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Meta is the metadata of one photo or video file as indexed by the gallery.
type Meta struct {
	Kind string // "photo" | "video"

	// TakenAt is the capture moment in UTC; nil when the file carries no
	// usable date. When the file has no UTC offset (TZOffsetMin == nil) the
	// camera's wall-clock time is stored as if it were UTC: the offset cannot
	// be derived reliably, and the indexer keeps the wall-clock value in a
	// separate taken_local column, so no extra flag is needed here.
	TakenAt *time.Time
	// TZOffsetMin is the UTC offset of the place of capture, in minutes, when
	// the file says so (OffsetTimeOriginal, QuickTime CreationDate).
	TZOffsetMin *int
	// DateFromXMP is true when TakenAt came from an XMP sidecar.
	DateFromXMP bool

	Lat, Lon *float64

	Make, Model, Lens string
	FNumber, FocalMM  float64
	ExposureS         string
	ISO               int

	Width, Height int // as displayed, i.e. after applying Orientation
	Orientation   int
	DurationMS    int64 // video only

	// HDR is true when the file carries an HDR gain map: Apple HDRGain tag,
	// HDRGainMap* tags (Apple HEIC auxiliary map) or an XMP hdrgm namespace
	// (Adobe/Google Ultra HDR). Deliberately conservative: a plain MPF
	// container without any of those markers does not count, and
	// HDRHeadroom alone is ignored.
	HDR bool
	// LiveCID is the Apple ContentIdentifier linking a Live Photo still to its
	// video; found by tag name in any group (Apple maker notes or QuickTime Keys).
	LiveCID string
	// HasPreview is true for RAW files with an embedded preview big enough to
	// show instead of decoding the RAW: PreviewImage/JpgFromRaw/OtherImage
	// longer than rawPreviewMinBytes. exiftool -n reports only the byte length
	// of these blobs, not pixel size, so the byte size is the proxy: a JPEG
	// under 200 kB is practically never 1024 px on its long side.
	HasPreview bool
	// BaselineExposure is the DNG's exposure correction in EV (Apple ProRAW: about +4),
	// applied when a RAW without a big preview has to be developed.
	BaselineExposure float64
	VideoCodec       string // "h264" | "hevc" | ...
}

const rawPreviewMinBytes = 200000

// DefaultExifTimeout bounds one exiftool read (PHOTOS_EXIF_TIMEOUT overrides it).
const DefaultExifTimeout = 60 * time.Second

// ErrExifTimeout is returned for a file exiftool did not answer for within the timeout.
// The process is killed (the next read starts a new one), so one bad file cannot hold up
// the indexer, the thumbnail workers and the viewer, which all share the reader.
var ErrExifTimeout = errors.New("exiftool timed out")

// Exif reads metadata through one long-lived `exiftool -stay_open` process.
// The process is started lazily, restarted after a crash, a cancelled read or a
// timeout, and Read is serialised by a mutex.
type Exif struct {
	bin string
	// Timeout bounds each read (a file and its sidecar are two reads); zero or less
	// means DefaultExifTimeout. Set it before the first Read.
	Timeout time.Duration

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	pipe   io.ReadCloser // under stdout: closed on cancel, so a blocked read returns
	closed bool
}

// NewExif prepares a reader and starts the exiftool process.
func NewExif(tools Tools) (*Exif, error) {
	if tools.Exiftool == "" {
		return nil, errors.New("exiftool not found")
	}
	e := &Exif{bin: tools.Exiftool}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.startLocked(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Exif) startLocked() error {
	cmd := exec.Command(e.bin, "-stay_open", "True", "-@", "-")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// stderr is not used: errors come back inside the JSON ("ExifTool:Error").
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start exiftool: %w", err)
	}
	e.cmd, e.stdin, e.stdout, e.pipe = cmd, stdin, bufio.NewReaderSize(stdout, 64<<10), stdout
	return nil
}

// killLocked drops the current process; the next Read starts a new one.
func (e *Exif) killLocked() {
	if e.cmd == nil {
		return
	}
	_ = e.cmd.Process.Kill()
	_ = e.stdin.Close()
	_ = e.pipe.Close()
	_ = e.cmd.Wait()
	e.cmd, e.stdin, e.stdout, e.pipe = nil, nil, nil, nil
}

// Close asks exiftool to exit and waits briefly before killing it.
func (e *Exif) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.cmd == nil {
		return nil
	}
	_, _ = io.WriteString(e.stdin, "-stay_open\nFalse\n")
	_ = e.stdin.Close()
	done := make(chan struct{})
	cmd := e.cmd
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	e.cmd, e.stdin, e.stdout, e.pipe = nil, nil, nil, nil
	return nil
}

var errExifDied = errors.New("exiftool process died")

// run executes one exiftool command and returns the JSON object of the file.
func (e *Exif) run(ctx context.Context, path string) (map[string]any, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.closed {
			return nil, errors.New("exif reader is closed")
		}
		if e.cmd == nil {
			if err := e.startLocked(); err != nil {
				return nil, err
			}
		}
		timeout := e.Timeout
		if timeout <= 0 {
			timeout = DefaultExifTimeout
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		out, err := e.exchangeLocked(rctx, path)
		timedOut := errors.Is(rctx.Err(), context.DeadlineExceeded)
		cancel()
		if err == nil {
			return parseExifJSON(out)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if timedOut {
			// The process was killed by the timeout; the next read starts a new one.
			return nil, fmt.Errorf("%w after %s", ErrExifTimeout, timeout)
		}
		// A crashed process is replaced; retry once on the fresh one.
		if attempt >= 1 || !errors.Is(err, errExifDied) {
			return nil, err
		}
	}
}

func (e *Exif) exchangeLocked(ctx context.Context, path string) ([]byte, error) {
	if !filepath.IsAbs(path) && strings.HasPrefix(path, "-") {
		path = "./" + path
	}
	if strings.ContainsAny(path, "\r\n") {
		// A newline would split the path into extra arguments in the -@ stream.
		// Read through a symlink with a safe name instead.
		dir, err := os.MkdirTemp("", "exif-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		link := filepath.Join(dir, "f"+filepath.Ext(path))
		if strings.ContainsAny(filepath.Ext(path), "\r\n") {
			link = filepath.Join(dir, "f")
		}
		if err := os.Symlink(abs, link); err != nil {
			return nil, err
		}
		path = link
	}
	req := "-charset\nfilename=utf8\n-json\n-n\n-G1\n-struct\n-api\nlargefilesupport=1\n" + path + "\n-execute\n"

	// On cancellation the process is killed, which unblocks the read below and
	// keeps the protocol from desynchronising (a half-answered command is lost).
	finished := make(chan struct{})
	cancelled := make(chan struct{})
	cmd, pipe := e.cmd, e.pipe
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			// A child that inherited stdout would keep the read below blocked.
			_ = pipe.Close()
			close(cancelled)
		case <-finished:
			close(cancelled)
		}
	}()
	out, err := e.exchangeIO(req)
	close(finished)
	<-cancelled

	if err != nil || ctx.Err() != nil {
		e.killLocked()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", errExifDied, err)
	}
	return out, nil
}

func (e *Exif) exchangeIO(req string) ([]byte, error) {
	if _, err := io.WriteString(e.stdin, req); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for {
		line, err := e.stdout.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		if string(bytes.TrimRight(line, "\r\n")) == "{ready}" {
			return buf.Bytes(), nil
		}
		buf.Write(line)
	}
}

func parseExifJSON(out []byte) (map[string]any, error) {
	var arr []map[string]any
	if err := json.Unmarshal(out, &arr); err != nil {
		return nil, fmt.Errorf("exiftool output: %w", err)
	}
	if len(arr) == 0 {
		return nil, errors.New("exiftool returned no data")
	}
	return arr[0], nil
}

// Read extracts the metadata of path. An XMP sidecar next to the file
// (<name>.xmp or <stem>.xmp) overrides the capture date and GPS position.
// For files exiftool cannot fully parse, the returned Meta is still usable
// (Kind is set from the extension) and the error describes the problem.
func (e *Exif) Read(ctx context.Context, path string) (Meta, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	m, err := e.run(ctx, path)
	if err != nil {
		return Meta{Kind: kindFromExt(path)}, err
	}
	meta := metaFromTags(m, path)

	if side := sidecarFor(path); side != "" {
		if sm, err := e.run(ctx, side); err == nil {
			if msg, _ := sm["ExifTool:Error"].(string); msg != "" {
				log.Printf("discodrive: photos: unreadable sidecar %s: %s", side, msg)
			}
			applySidecar(&meta, sm)
		} else if ctx.Err() != nil {
			return meta, ctx.Err()
		} else {
			// The file keeps its own values, but a sidecar nobody can read is worth a trace.
			log.Printf("discodrive: photos: unreadable sidecar %s: %v", side, err)
		}
	}
	if msg, ok := m["ExifTool:Error"].(string); ok && msg != "" {
		return meta, fmt.Errorf("exiftool: %s", msg)
	}
	return meta, nil
}

func sidecarFor(path string) string {
	ext := filepath.Ext(path)
	for _, c := range []string{path + ".xmp", strings.TrimSuffix(path, ext) + ".xmp"} {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

var videoExts = map[string]bool{
	".mp4": true, ".mov": true, ".m4v": true, ".avi": true, ".mkv": true,
	".webm": true, ".3gp": true, ".mts": true, ".m2ts": true, ".mpg": true, ".mpeg": true,
}

var rawExts = map[string]bool{
	".dng": true, ".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".orf": true,
	".rw2": true, ".raf": true, ".pef": true, ".srw": true,
}

func kindFromExt(path string) string {
	if videoExts[strings.ToLower(filepath.Ext(path))] {
		return "video"
	}
	return "photo"
}

// tag lookup ------------------------------------------------------------

// lookup finds a tag by its name regardless of group (-G1 yields names such as
// "IFD0:Model", "Keys:Model"). Groups listed in prefer win, in that order;
// otherwise the alphabetically first group is used, so results are stable.
func lookup(m map[string]any, tag string, prefer ...string) (any, bool) {
	for _, g := range prefer {
		if v, ok := m[g+":"+tag]; ok {
			return v, true
		}
	}
	var keys []string
	suffix := ":" + tag
	for k := range m {
		if strings.HasSuffix(k, suffix) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	return m[keys[0]], true
}

func str(m map[string]any, tag string, prefer ...string) string {
	v, ok := lookup(m, tag, prefer...)
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case []any:
		if len(x) > 0 {
			return toFloat(x[0])
		}
	}
	return 0, false
}

func num(m map[string]any, tag string, prefer ...string) (float64, bool) {
	v, ok := lookup(m, tag, prefer...)
	if !ok {
		return 0, false
	}
	return toFloat(v)
}

// dates -----------------------------------------------------------------

var dateRe = regexp.MustCompile(`^(\d{4}):(\d{2}):(\d{2})[ T](\d{2}):(\d{2}):(\d{2})(?:\.\d+)?\s*(Z|[+-]\d{2}:?\d{2})?$`)

// parseDate parses an exiftool date. It returns the instant (UTC when an
// offset is present, otherwise the wall-clock time labelled UTC) and the
// offset in minutes when known. Zero dates and years before 1900 are rejected.
func parseDate(s string) (t time.Time, off *int, ok bool) {
	s = strings.TrimSpace(s)
	g := dateRe.FindStringSubmatch(s)
	if g == nil {
		return t, nil, false
	}
	var p [6]int
	for i := range p {
		p[i], _ = strconv.Atoi(g[i+1])
	}
	if p[0] < 1900 || p[1] < 1 || p[1] > 12 || p[2] < 1 || p[2] > 31 || p[3] > 23 || p[4] > 59 || p[5] > 60 {
		return t, nil, false
	}
	wall := time.Date(p[0], time.Month(p[1]), p[2], p[3], p[4], p[5], 0, time.UTC)
	if wall.Day() != p[2] { // e.g. 02-31 normalised into March
		return t, nil, false
	}
	switch z := g[7]; {
	case z == "":
		return wall, nil, true
	case z == "Z":
		o := 0
		return wall, &o, true
	default:
		sign := 1
		if z[0] == '-' {
			sign = -1
		}
		d := strings.ReplaceAll(z[1:], ":", "")
		h, _ := strconv.Atoi(d[:2])
		mi, _ := strconv.Atoi(d[2:])
		o := sign * (h*60 + mi)
		return wall.Add(-time.Duration(o) * time.Minute), &o, true
	}
}

// dateWithOffset combines a date string with a separate OffsetTime* value when
// the date itself carries no zone.
func dateWithOffset(date, offset string) (time.Time, *int, bool) {
	t, off, ok := parseDate(date)
	if !ok {
		return t, nil, false
	}
	if off == nil && offset != "" {
		if _, o, ok2 := parseDate("2000:01:01 00:00:00" + offset); ok2 && o != nil {
			// Re-interpret the wall time in the zone.
			return t.Add(-time.Duration(*o) * time.Minute), o, true
		}
	}
	return t, off, true
}

// GPS -------------------------------------------------------------------

var iso6709Re = regexp.MustCompile(`^([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)`)

func validCoords(lat, lon float64) bool {
	if math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return false
	}
	return !(lat == 0 && lon == 0)
}

func coords(m map[string]any) (lat, lon *float64) {
	la, ok1 := num(m, "GPSLatitude")
	lo, ok2 := num(m, "GPSLongitude")
	if !ok1 || !ok2 {
		// QuickTime Keys:GPSCoordinates, ISO 6709: "+42.6475+077.0820+0650.000/"
		if g := iso6709Re.FindStringSubmatch(str(m, "GPSCoordinates")); g != nil {
			la, _ = strconv.ParseFloat(g[1], 64)
			lo, _ = strconv.ParseFloat(g[2], 64)
			ok1, ok2 = true, true
		}
	}
	if ok1 && ok2 && validCoords(la, lo) {
		return &la, &lo
	}
	return nil, nil
}

// assembling ------------------------------------------------------------

func metaFromTags(m map[string]any, path string) Meta {
	var meta Meta

	mime := str(m, "MIMEType", "File")
	switch {
	case strings.HasPrefix(mime, "video/"):
		meta.Kind = "video"
	case strings.HasPrefix(mime, "image/"):
		meta.Kind = "photo"
	default:
		meta.Kind = kindFromExt(path)
	}

	meta.Make = str(m, "Make", "IFD0", "Keys")
	meta.Model = str(m, "Model", "IFD0", "Keys")
	meta.Lens = str(m, "LensModel", "ExifIFD")
	if meta.Lens == "" {
		meta.Lens = str(m, "LensID")
	}
	meta.FNumber, _ = num(m, "FNumber", "ExifIFD")
	meta.FocalMM, _ = num(m, "FocalLength", "ExifIFD")
	if v, ok := num(m, "ExposureTime", "ExifIFD"); ok && v > 0 {
		meta.ExposureS = formatExposure(v)
	}
	if v, ok := num(m, "ISO", "ExifIFD"); ok {
		meta.ISO = int(v)
	}

	// Date: video prefers QuickTime CreationDate (has a zone) over CreateDate (UTC).
	var dates []string
	if meta.Kind == "video" {
		dates = []string{"CreationDate", "DateTimeOriginal", "CreateDate", "MediaCreateDate", "TrackCreateDate"}
	} else {
		dates = []string{"DateTimeOriginal", "CreateDate", "DateTimeCreated"}
	}
	offset := str(m, "OffsetTimeOriginal")
	if offset == "" {
		offset = str(m, "OffsetTime")
	}
	for _, tag := range dates {
		if s := str(m, tag); s != "" {
			if t, off, ok := dateWithOffset(s, offset); ok {
				t = t.UTC()
				meta.TakenAt, meta.TZOffsetMin = &t, off
				break
			}
		}
	}

	meta.Lat, meta.Lon = coords(m)

	// Dimensions and orientation.
	if o, ok := num(m, "Orientation", "IFD0"); ok && o >= 1 && o <= 8 {
		meta.Orientation = int(o)
	}
	w, h := dimensions(m, meta.Kind)
	if meta.Orientation >= 5 {
		w, h = h, w
	}
	if meta.Kind == "video" {
		// QuickTime track rotation: 90/270 means the stored frame is displayed turned.
		if r, ok := num(m, "Rotation"); ok {
			if d := ((int(math.Round(r)) % 360) + 360) % 360; d == 90 || d == 270 {
				w, h = h, w
			}
		}
	}
	meta.Width, meta.Height = w, h

	if d, ok := num(m, "Duration", "QuickTime"); ok && meta.Kind == "video" {
		meta.DurationMS = int64(math.Round(d * 1000))
	}
	if meta.Kind == "video" {
		meta.VideoCodec = videoCodec(m)
	}

	meta.LiveCID = str(m, "ContentIdentifier", "Apple", "Keys", "QuickTime")
	meta.HDR = detectHDR(m)

	if rawExts[strings.ToLower(filepath.Ext(path))] {
		for _, tag := range []string{"PreviewImageLength", "JpgFromRawLength", "OtherImageLength"} {
			if n, ok := num(m, tag); ok && n > rawPreviewMinBytes {
				meta.HasPreview = true
			}
		}
		if be, ok := num(m, "BaselineExposure"); ok && !math.IsNaN(be) {
			meta.BaselineExposure = be
		}
	}
	return meta
}

func dimensions(m map[string]any, kind string) (w, h int) {
	pairs := [][2]string{{"File:ImageWidth", "File:ImageHeight"}}
	if kind == "video" {
		pairs = [][2]string{{"Track1:ImageWidth", "Track1:ImageHeight"}, {"QuickTime:ImageWidth", "QuickTime:ImageHeight"}}
	}
	pairs = append(pairs,
		[2]string{"IFD0:ImageWidth", "IFD0:ImageHeight"},
		[2]string{"ExifIFD:ExifImageWidth", "ExifIFD:ExifImageHeight"},
	)
	for _, p := range pairs {
		wf, ok1 := toFloat(m[p[0]])
		hf, ok2 := toFloat(m[p[1]])
		if ok1 && ok2 && wf > 0 && hf > 0 {
			return int(wf), int(hf)
		}
	}
	wf, ok1 := num(m, "ImageWidth")
	hf, ok2 := num(m, "ImageHeight")
	if ok1 && ok2 {
		return int(wf), int(hf)
	}
	return 0, 0
}

func videoCodec(m map[string]any) string {
	var keys []string
	for k := range m {
		if strings.HasSuffix(k, ":CompressorID") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		s, _ := m[k].(string)
		switch c := strings.ToLower(strings.TrimSpace(s)); c {
		case "":
		case "avc1", "avc3", "h264":
			return "h264"
		case "hvc1", "hev1", "hevc":
			return "hevc"
		case "vp09":
			return "vp9"
		case "av01":
			return "av1"
		case "mp4v":
			return "mpeg4"
		default:
			return c
		}
	}
	return ""
}

func detectHDR(m map[string]any) bool {
	for k := range m {
		group, tag, _ := strings.Cut(k, ":")
		if tag == "HDRGain" || strings.HasPrefix(tag, "HDRGainMap") || strings.EqualFold(group, "XMP-hdrgm") {
			return true
		}
	}
	return false
}

func formatExposure(s float64) string {
	if s < 1 {
		return "1/" + strconv.Itoa(int(math.Round(1/s)))
	}
	return strconv.FormatFloat(s, 'f', -1, 64)
}

// applySidecar overrides date and GPS with the XMP sidecar's values.
func applySidecar(meta *Meta, sm map[string]any) {
	if s := str(sm, "DateTimeOriginal", "XMP-exif", "XMP-photoshop"); s != "" {
		if t, off, ok := parseDate(s); ok {
			t = t.UTC()
			meta.TakenAt, meta.TZOffsetMin, meta.DateFromXMP = &t, off, true
		}
	}
	if lat, lon := coords(sm); lat != nil {
		meta.Lat, meta.Lon = lat, lon
	}
}
