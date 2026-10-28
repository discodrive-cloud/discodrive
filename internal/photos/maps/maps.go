// Package maps keeps the gallery's own map data on disk: a regional PMTiles extract of
// the Protomaps basemap, its fonts and sprites, and the GeoNames files the offline
// geocoder reads. Nothing is bundled; an admin picks a region and the server downloads
// it. No external tile service is ever contacted by browsers.
package maps

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"discodrive/internal/db"
	"discodrive/internal/photos/geo"
	"github.com/jackc/pgx/v5"
)

// Region is a downloadable part of the world. BBox is "minLon,minLat,maxLon,maxLat";
// empty means the whole planet.
type Region struct {
	ID      string `json:"id"`
	BBox    string `json:"bbox"`
	MaxZoom int8   `json:"maxzoom"`
	// ApproxBytes is the expected size of the extract. centralasia at zoom 14 was
	// measured (2.14 GB); the others are ESTIMATES. It drives the free-space check and
	// the hard stop (twice this size).
	ApproxBytes int64 `json:"approxBytes"`
}

// Regions is what an admin can choose from.
var Regions = []Region{
	{ID: "centralasia", BBox: "46,35,88,56", MaxZoom: 14, ApproxBytes: 2_200_000_000},
	{ID: "europe", BBox: "-25,34,45,72", MaxZoom: 12, ApproxBytes: 6_000_000_000},
	{ID: "world", BBox: "", MaxZoom: 10, ApproxBytes: 5_000_000_000},
}

// FindRegion returns the region with the id.
func FindRegion(id string) (Region, bool) {
	for _, r := range Regions {
		if r.ID == id {
			return r, true
		}
	}
	return Region{}, false
}

const (
	// settings keys: the chosen region id, and the JSON state of its download
	KeyRegion = "photos.maps.region"
	KeyState  = "photos.maps.state"

	DefaultBuildsURL = "https://build.protomaps.com"
	DefaultGeoURL    = "https://download.geonames.org/export/dump"
	// assetsCommit pins the protomaps/basemaps-assets revision (fonts + sprites).
	assetsCommit = "028c18f713baecad011301ff7a69acc39bcc2ae7"
	// buildDays is how many days back the newest daily build is looked for.
	buildDays = 7
	// stallTimeout: a download that moves no bytes for this long fails.
	stallTimeout = 2 * time.Minute
	// freeFloor is what must stay free on top of 1.2x the expected size.
	freeFloor = 1 << 30
	// maxAssetBytes bounds what is unpacked from the assets archive.
	maxAssetBytes = 1 << 30
)

// Download states.
const (
	StateNone        = "none"
	StateDownloading = "downloading"
	StateReady       = "ready"
	StateFailed      = "failed"
	// StateUnavailable is reported when the pmtiles tool is not installed (lite image).
	StateUnavailable = "unavailable"
)

// Status is the persisted/served download state.
type Status struct {
	Region string `json:"region"`
	State  string `json:"state"`
	Bytes  int64  `json:"bytes"`
	Error  string `json:"error"`
}

type persisted struct {
	State string `json:"state"`
	Bytes int64  `json:"bytes"`
	Error string `json:"error,omitempty"`
}

// Config points the downloader at its sources; zero values mean the real ones.
type Config struct {
	// Pmtiles is the path of the pmtiles CLI (go-pmtiles); empty = maps unavailable
	// unless Extract is set (tests).
	Pmtiles      string
	BuildsURL    string
	GeoURL       string
	AssetsZipURL string
	Client       *http.Client
	// Extract cuts bbox/maxzoom out of the remote archive src into the file out.
	Extract func(ctx context.Context, src, bbox string, maxzoom int8, out string) error
	// Free returns the bytes free on the volume holding dir (default: statfs).
	Free func(dir string) (uint64, error)
	// Stall, Tick and RestartAfter tune the watchdogs (defaults: 2 min, 1 s, 30 s).
	Stall, Tick, RestartAfter time.Duration
}

// Manager owns the download and the files.
type Manager struct {
	q    *db.Queries
	dir  string // <StorageRoot>/.photos/maps
	geo  string // <StorageRoot>/.photos/geo
	cfg  Config
	base context.Context

	// OnGeo, when set (before the first Set/Resume), is called once the GeoNames files are
	// on disk (and at startup when they already are). It must not block for long.
	OnGeo func()

	approx map[string]int64 // expected extract size per region (tests lower it)
	setMu  sync.Mutex       // serialises Set: the latest request wins
	last   atomic.Int64     // unix nanos of the last sign of download progress

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
	region  string
	bytes   atomic.Int64
}

// New creates a manager; base bounds every background download (server lifetime).
func New(base context.Context, q *db.Queries, storageRoot string, cfg Config) *Manager {
	if cfg.BuildsURL == "" {
		cfg.BuildsURL = DefaultBuildsURL
	}
	if cfg.GeoURL == "" {
		cfg.GeoURL = DefaultGeoURL
	}
	if cfg.AssetsZipURL == "" {
		cfg.AssetsZipURL = "https://codeload.github.com/protomaps/basemaps-assets/zip/" + assetsCommit
	}
	if cfg.Stall == 0 {
		cfg.Stall = stallTimeout
	}
	if cfg.Tick == 0 {
		cfg.Tick = time.Second
	}
	if cfg.RestartAfter == 0 {
		cfg.RestartAfter = 30 * time.Second
	}
	if cfg.Free == nil {
		cfg.Free = freeBytes
	}
	if cfg.Client == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = time.Minute
		cfg.Client = &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("redirect from https to http refused")
			}
			return nil
		}}
	}
	if cfg.Extract == nil && cfg.Pmtiles != "" {
		cfg.Extract = cliExtract(cfg.Pmtiles)
	}
	approx := map[string]int64{}
	for _, r := range Regions {
		approx[r.ID] = r.ApproxBytes
	}
	return &Manager{q: q, base: base, cfg: cfg, approx: approx,
		dir: filepath.Join(storageRoot, ".photos", "maps"), geo: filepath.Join(storageRoot, ".photos", "geo")}
}

// GeoDir is where the GeoNames files live.
func (m *Manager) GeoDir() string { return m.geo }

// RegionFile is the tiles archive path, whether or not it is ready.
func (m *Manager) RegionFile() string { return filepath.Join(m.dir, "region.pmtiles") }

// AssetsDir is the fonts/sprites directory.
func (m *Manager) AssetsDir() string { return filepath.Join(m.dir, "assets") }

// Available reports whether the pmtiles tool was found; without it nothing downloads.
func (m *Manager) Available() bool { return m.cfg.Extract != nil }

// cliExtract runs `pmtiles extract <url> <out> --bbox=... --maxzoom=N` (go-pmtiles CLI).
func cliExtract(bin string) func(ctx context.Context, src, bbox string, maxzoom int8, out string) error {
	return func(ctx context.Context, src, bbox string, maxzoom int8, out string) error {
		args := []string{"extract", "--quiet", fmt.Sprintf("--maxzoom=%d", maxzoom)}
		if bbox != "" {
			args = append(args, "--bbox="+bbox)
		}
		cmd := exec.CommandContext(ctx, bin, append(args, src, out)...)
		var stderr limitedBuf
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

// limitedBuf keeps the first 2 KB written to it (a tool's error output).
type limitedBuf struct{ b []byte }

func (l *limitedBuf) Write(p []byte) (int, error) {
	if room := 2048 - len(l.b); room > 0 {
		l.b = append(l.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
func (l *limitedBuf) String() string { return string(l.b) }

func (m *Manager) load(ctx context.Context) (Status, error) {
	st := Status{State: StateNone}
	r, err := m.q.GetSetting(ctx, KeyRegion)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, nil
	} else if err != nil {
		return st, err
	}
	st.Region = r.Value
	s, err := m.q.GetSetting(ctx, KeyState)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, nil
	} else if err != nil {
		return st, err
	}
	var p persisted
	if json.Unmarshal([]byte(s.Value), &p) == nil && p.State != "" {
		st.State, st.Bytes, st.Error = p.State, p.Bytes, p.Error
	}
	return st, nil
}

func (m *Manager) save(ctx context.Context, region string, p persisted) {
	b, _ := json.Marshal(p)
	for k, v := range map[string]string{KeyRegion: region, KeyState: string(b)} {
		if err := m.q.UpsertSetting(ctx, db.UpsertSettingParams{Key: k, Value: v}); err != nil {
			log.Printf("discodrive: photos maps: saving %s: %v", k, err)
		}
	}
}

// Status is the current state; while a download runs the byte count is live.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	st, err := m.load(ctx)
	if err != nil {
		return st, err
	}
	if !m.Available() && st.State != StateReady {
		st.State = StateUnavailable
	}
	m.mu.Lock()
	running := m.running
	m.mu.Unlock()
	if running && st.State == StateDownloading {
		st.Bytes = m.bytes.Load()
	}
	return st, nil
}

// Ready reports whether the tiles can be served (the user-facing answer).
func (m *Manager) Ready(ctx context.Context) bool {
	st, err := m.load(ctx)
	if err != nil || st.State != StateReady {
		return false
	}
	_, err = os.Stat(m.RegionFile())
	return err == nil
}

// ErrUnknownRegion is returned by Set for an id outside Regions; ErrUnavailable when the
// pmtiles tool is missing.
var (
	ErrUnknownRegion = errors.New("unknown region")
	ErrUnavailable   = errors.New("maps are unavailable: pmtiles tool not found")
)

// Set starts downloading the region ("" removes the maps). Calls are serialised and the
// latest one wins: it cancels whatever runs, then replaces it. Asking again for the
// region that is ready changes nothing, and for one that is downloading only while it is
// making progress (a stalled one is restarted).
func (m *Manager) Set(ctx context.Context, regionID string) error {
	var reg Region
	if regionID != "" {
		var ok bool
		if reg, ok = FindRegion(regionID); !ok {
			return ErrUnknownRegion
		}
		if !m.Available() {
			return ErrUnavailable
		}
	}
	m.setMu.Lock()
	defer m.setMu.Unlock()
	m.mu.Lock()
	running, cur := m.running, m.region
	m.mu.Unlock()
	if running && cur == regionID && time.Since(time.Unix(0, m.last.Load())) < m.cfg.RestartAfter {
		return nil
	}
	m.stop()
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.load(ctx)
	if err != nil {
		return err
	}
	if regionID == "" {
		_ = m.q.DeleteSetting(ctx, KeyRegion)
		_ = m.q.DeleteSetting(ctx, KeyState)
		return os.RemoveAll(m.dir)
	}
	if st.Region == regionID && st.State == StateReady {
		if _, err := os.Stat(m.RegionFile()); err == nil {
			return nil
		}
	}
	if st.Region != regionID {
		_ = os.Remove(m.RegionFile()) // the old region's tiles must not outlive the choice
	}
	m.start(reg)
	return nil
}

// Resume runs at startup: a download interrupted by a restart is started again from
// scratch (an extract cannot continue from a partial file); a finished one just makes
// the geocoder load.
func (m *Manager) Resume(ctx context.Context) {
	st, err := m.load(ctx)
	if err != nil || st.Region == "" {
		m.geoReady()
		return
	}
	if st.State == StateDownloading {
		if reg, ok := FindRegion(st.Region); ok && m.Available() {
			m.mu.Lock()
			if !m.running {
				m.start(reg)
			}
			m.mu.Unlock()
			return
		}
	}
	m.geoReady()
}

// Wait blocks until a running download ends (tests, shutdown).
func (m *Manager) Wait() {
	m.mu.Lock()
	d := m.done
	m.mu.Unlock()
	if d != nil {
		<-d
	}
}

// stop cancels a running download and waits for it.
func (m *Manager) stop() {
	m.mu.Lock()
	c, d := m.cancel, m.done
	m.mu.Unlock()
	if c != nil {
		c()
		<-d
	}
}

// start must be called with m.mu held and nothing running.
func (m *Manager) start(reg Region) {
	ctx, cancel := context.WithCancel(m.base)
	m.cancel, m.done, m.running, m.region = cancel, make(chan struct{}), true, reg.ID
	m.bytes.Store(0)
	m.last.Store(time.Now().UnixNano())
	m.save(ctx, reg.ID, persisted{State: StateDownloading})
	done := m.done
	go func() {
		err := m.run(ctx, reg)
		m.mu.Lock()
		defer m.mu.Unlock()
		defer close(done)
		m.running = false
		switch {
		case ctx.Err() != nil:
			// cancelled by a new choice or by shutdown: the next Set/Resume owns the state
			// (after a shutdown the "downloading" state is what makes Resume restart it)
		case err != nil:
			log.Printf("discodrive: photos maps: %v", err)
			m.save(m.base, reg.ID, persisted{State: StateFailed, Error: err.Error()})
		default:
			m.save(m.base, reg.ID, persisted{State: StateReady, Bytes: m.bytes.Load()})
		}
	}()
}

func (m *Manager) geoReady() {
	if m.OnGeo == nil {
		return
	}
	if _, err := os.Stat(filepath.Join(m.geo, "cities500.zip")); err == nil {
		m.OnGeo()
	}
}

func (m *Manager) run(ctx context.Context, reg Region) error {
	if err := os.MkdirAll(m.dir, 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(m.geo, 0o750); err != nil {
		return err
	}
	// 1. places: small, and useful even if the tiles fail
	for _, f := range []string{geo.CitiesZip} {
		if err := m.fetchFile(ctx, m.cfg.GeoURL+"/"+f, filepath.Join(m.geo, f)); err != nil {
			return fmt.Errorf("geonames %s: %w", f, err)
		}
	}
	if err := m.fetchNames(ctx); err != nil && ctx.Err() == nil {
		log.Printf("discodrive: photos maps: translated place names skipped: %v", err) // places keep GeoNames' names
	}
	if m.OnGeo != nil {
		m.OnGeo()
	}
	// 2. fonts and sprites
	if err := m.fetchAssets(ctx); err != nil {
		return fmt.Errorf("assets: %w", err)
	}
	// 3. tiles
	need := uint64(float64(m.approx[reg.ID])*1.2) + freeFloor
	if free, err := m.cfg.Free(m.dir); err == nil && free < need {
		return fmt.Errorf("not enough free disk space: %d MB free, %d MB needed", free>>20, need>>20)
	}
	src, err := m.latestBuild(ctx)
	if err != nil {
		return err
	}
	out := m.RegionFile()
	part := out + ".part"
	_ = os.Remove(part)
	ectx, cancel := context.WithCancel(ctx)
	defer cancel()
	var guardErr atomic.Value // error that made the watchdog cancel the extract
	stop := make(chan struct{})
	watchDone := make(chan struct{})
	go func() { // progress = size of the file being written, plus the two guards
		defer close(watchDone)
		t := time.NewTicker(m.cfg.Tick)
		defer t.Stop()
		began, size := time.Now(), int64(-1)
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			fi, err := os.Stat(part)
			switch {
			case err == nil && fi.Size() != size:
				size = fi.Size()
				m.bytes.Store(size)
				m.last.Store(time.Now().UnixNano())
				if limit := 2 * m.approx[reg.ID]; limit > 0 && size > limit {
					guardErr.Store(fmt.Errorf("extract grew past %d MB (twice the expected size), stopped", limit>>20))
					cancel()
					return
				}
			case err == nil && time.Since(time.Unix(0, m.last.Load())) > m.cfg.Stall,
				err != nil && time.Since(began) > 10*m.cfg.Stall:
				// (the tool reads the archive's directories before the first byte is
				// written, hence the longer allowance until the file exists)
				guardErr.Store(errors.New("extract stalled: no progress"))
				cancel()
				return
			}
		}
	}()
	err = m.cfg.Extract(ectx, src, reg.BBox, reg.MaxZoom, part)
	close(stop)
	<-watchDone
	if g, ok := guardErr.Load().(error); ok {
		err = g
	} else if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = os.Remove(part)
		return fmt.Errorf("extract %s: %w", reg.ID, err)
	}
	fi, err := os.Stat(part)
	if err != nil {
		return err
	}
	m.bytes.Store(fi.Size())
	return os.Rename(part, out)
}

// latestBuild finds the newest daily build of the last buildDays days by HEAD requests.
func (m *Manager) latestBuild(ctx context.Context) (string, error) {
	now := time.Now().UTC()
	for i := 0; i < buildDays; i++ {
		u := fmt.Sprintf("%s/%s.pmtiles", m.cfg.BuildsURL, now.AddDate(0, 0, -i).Format("20060102"))
		hctx, hcancel := context.WithTimeout(ctx, 30*time.Second)
		req, err := http.NewRequestWithContext(hctx, http.MethodHead, u, nil)
		if err != nil {
			hcancel()
			return "", err
		}
		resp, err := m.cfg.Client.Do(req)
		hcancel()
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return u, nil
		}
	}
	return "", fmt.Errorf("no protomaps build in the last %d days", buildDays)
}

// fetchFile downloads url to dest through dest.part. An existing dest is kept.
func (m *Manager) fetchFile(ctx context.Context, url, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	part := dest + ".part"
	if err := m.get(ctx, url, part); err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, dest)
}

// get downloads url into the file to. A transfer that moves no bytes for cfg.Stall fails,
// however slowly the rest goes.
func (m *Manager) get(ctx context.Context, url, to string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var progress atomic.Int64
	progress.Store(time.Now().UnixNano())
	var stalled atomic.Bool
	tick := min(m.cfg.Tick, m.cfg.Stall/4)
	if tick <= 0 {
		tick = time.Second
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if time.Since(time.Unix(0, progress.Load())) > m.cfg.Stall {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	err := func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := m.cfg.Client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
		}
		f, err := os.Create(to)
		if err != nil {
			return err
		}
		buf := make([]byte, 64<<10)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				progress.Store(time.Now().UnixNano())
				if _, werr := f.Write(buf[:n]); werr != nil {
					f.Close()
					return werr
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				f.Close()
				return rerr
			}
		}
		return f.Close()
	}()
	if err != nil && stalled.Load() {
		return fmt.Errorf("%s: stalled, no data for %v", url, m.cfg.Stall)
	}
	return err
}

// fetchNames distils the alternate-names dump into geo.Names: the ~200 MB zip is
// downloaded once, read in a single pass and deleted. Skipped when the result exists.
func (m *Manager) fetchNames(ctx context.Context) error {
	out := filepath.Join(m.geo, geo.Names)
	if _, err := os.Stat(out); err == nil {
		return nil
	}
	zpath := filepath.Join(m.geo, geo.AltNamesZip+".part")
	defer os.Remove(zpath)
	if err := m.get(ctx, m.cfg.GeoURL+"/"+geo.AltNamesZip, zpath); err != nil {
		return err
	}
	return geo.BuildNames(zpath, filepath.Join(m.geo, geo.CitiesZip), out)
}

// fetchAssets unpacks fonts/ and sprites/ of the pinned basemaps-assets archive into
// AssetsDir (built next to it and renamed, so a half-unpacked tree never serves).
func (m *Manager) fetchAssets(ctx context.Context) error {
	dest := m.AssetsDir()
	if fi, err := os.Stat(filepath.Join(dest, "fonts")); err == nil && fi.IsDir() {
		return nil
	}
	zpath := filepath.Join(m.dir, "assets.zip.part")
	defer os.Remove(zpath)
	if err := m.get(ctx, m.cfg.AssetsZipURL, zpath); err != nil {
		return err
	}
	zr, err := zip.OpenReader(zpath)
	if err != nil {
		return err
	}
	defer zr.Close()
	tmp := dest + ".part"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o750); err != nil {
		return err
	}
	var total int64
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		// "<repo>-<sha>/fonts/..." -> "fonts/..."
		_, rel, ok := strings.Cut(f.Name, "/")
		if !ok || f.FileInfo().IsDir() || !(strings.HasPrefix(rel, "fonts/") || strings.HasPrefix(rel, "sprites/")) {
			continue
		}
		if path.Clean(rel) != rel || strings.Contains(rel, "..") || !f.Mode().IsRegular() {
			continue
		}
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		n, err := copyZipFile(f, target, maxAssetBytes-total)
		if err != nil {
			return err
		}
		total += n
	}
	if _, err := os.Stat(filepath.Join(tmp, "fonts")); err != nil {
		return errors.New("archive has no fonts/")
	}
	_ = os.RemoveAll(dest)
	return os.Rename(tmp, dest)
}

func copyZipFile(f *zip.File, target string, limit int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(rc, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("assets archive too large")
	}
	return n, err
}

// ServeRegion answers GET/HEAD for the tiles archive with Range support (PMTiles
// readers fetch it in byte ranges).
func (m *Manager) ServeRegion(w http.ResponseWriter, r *http.Request) {
	f, err := os.Open(m.RegionFile())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "private, no-cache")
	// A new region replaces the file; the ETag lets range clients notice the swap.
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, fi.Size(), fi.ModTime().UnixNano()))
	http.ServeContent(w, r, "region.pmtiles", fi.ModTime(), f)
}

var assetTypes = map[string]string{
	".pbf": "application/x-protobuf", ".json": "application/json", ".png": "image/png",
}

// ServeAsset serves a font or sprite file. rel is the path below the assets directory;
// the lookup goes through os.Root, so neither ".." nor a symlink can leave the directory.
func (m *Manager) ServeAsset(w http.ResponseWriter, r *http.Request, rel string) {
	if rel == "" || strings.Contains(rel, "\\") || strings.ContainsRune(rel, 0) {
		http.NotFound(w, r)
		return
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." || seg == "" {
			http.NotFound(w, r)
			return
		}
	}
	root, err := os.OpenRoot(m.AssetsDir())
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if ct, ok := assetTypes[strings.ToLower(path.Ext(rel))]; ok {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, path.Base(rel), fi.ModTime(), f)
}

func dirOf(p string) string { return filepath.Dir(p) }
