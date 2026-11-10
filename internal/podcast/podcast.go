// Package podcast fetches and parses podcast RSS feeds and downloads episode
// and cover files. All network access goes through an SSRF guard (see ssrf.go),
// except the *Unsafe variants used by tests with an explicit client.
package podcast

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

// Feed is a normalized podcast channel.
type Feed struct {
	Title       string
	Description string
	ImageURL    string
	Episodes    []Episode
}

// Episode is a normalized podcast episode from a feed.
type Episode struct {
	Title       string
	Description string
	PubDate     *time.Time
	AudioURL    string
	Duration    int // seconds, 0 if unknown
	ContentType string
}

// maxRedirects is net/http's own default; a custom CheckRedirect replaces that check, so
// it is repeated here or a host redirecting to itself would be followed forever.
const maxRedirects = 10

// checkRedirect re-validates every redirect target (a feed host must not 302 to a
// private IP) and bounds the chain.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("podcast: stopped after %d redirects", maxRedirects)
	}
	return ValidateURL(req.URL.String())
}

// guardedTransport is shared by every outbound podcast request, so connections are
// reused and idle ones are closed. safeDialer pins the SSRF check to the address
// actually dialed (DNS-rebinding safe).
var guardedTransport = &http.Transport{
	Proxy:                 nil, // an env proxy would bypass safeDialer's IP check
	DialContext:           safeDialer.DialContext,
	ResponseHeaderTimeout: 30 * time.Second,
	IdleConnTimeout:       90 * time.Second,
}

// guardedClient fetches feeds: small documents, so the whole exchange is bounded.
func guardedClient() *http.Client {
	return &http.Client{Timeout: httpTimeout, Transport: guardedTransport, CheckRedirect: checkRedirect}
}

// newDownloadClient fetches episodes and covers. It has no whole-exchange Timeout:
// http.Client.Timeout also covers reading the body, and an episode of a few hundred
// megabytes does not arrive in 30 seconds. The header phase is bounded by the
// transport, a hung body by stallTimeout in DownloadToUnsafe.
func newDownloadClient() *http.Client {
	return &http.Client{Transport: guardedTransport, CheckRedirect: checkRedirect}
}

// FetchFeed validates the URL (SSRF guard) then parses the feed.
func FetchFeed(ctx context.Context, feedURL string) (Feed, error) {
	if err := ValidateURL(feedURL); err != nil {
		return Feed{}, err
	}
	return FetchFeedUnsafe(ctx, guardedClient(), feedURL)
}

// FetchFeedUnsafe parses a feed using the given client WITHOUT the SSRF guard.
// WARNING: for tests only — callers are responsible for ensuring the URL is safe.
func FetchFeedUnsafe(ctx context.Context, client *http.Client, feedURL string) (Feed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return Feed{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Feed{}, err
	}
	defer resp.Body.Close()
	const maxFeedBytes = 16 << 20 // 16 MiB — sufficient for any real feed
	parsed, err := gofeed.NewParser().Parse(io.LimitReader(resp.Body, maxFeedBytes))
	if err != nil {
		return Feed{}, err
	}
	f := Feed{Title: parsed.Title, Description: parsed.Description}
	if parsed.Image != nil {
		f.ImageURL = parsed.Image.URL
	}
	if parsed.ITunesExt != nil && parsed.ITunesExt.Image != "" {
		f.ImageURL = parsed.ITunesExt.Image
	}
	for _, it := range parsed.Items {
		ep := Episode{Title: it.Title, Description: it.Description, PubDate: it.PublishedParsed}
		if len(it.Enclosures) > 0 {
			ep.AudioURL = it.Enclosures[0].URL
			ep.ContentType = it.Enclosures[0].Type
		}
		if ep.AudioURL == "" {
			continue // skip items with no audio
		}
		f.Episodes = append(f.Episodes, ep)
	}
	return f, nil
}

// DownloadTo validates the URL then downloads it to destPath.
func DownloadTo(ctx context.Context, srcURL, destPath string) (size int64, contentType, suffix string, err error) {
	return DownloadToLimit(ctx, srcURL, destPath, 0)
}

// DownloadToLimit is DownloadTo bounded by max bytes (0 = unbounded). A feed states no
// reliable size for its enclosures, so an episode can only be held to a limit while it
// streams; crossing it aborts the transfer and leaves nothing behind.
func DownloadToLimit(ctx context.Context, srcURL, destPath string, max int64) (size int64, contentType, suffix string, err error) {
	if err := ValidateURL(srcURL); err != nil {
		return 0, "", "", err
	}
	return DownloadToUnsafe(ctx, newDownloadClient(), srcURL, destPath, max)
}

// DownloadToUnsafe downloads srcURL to destPath using the given client WITHOUT the SSRF guard.
// WARNING: for tests only — callers are responsible for ensuring the URL is safe.
func DownloadToUnsafe(ctx context.Context, client *http.Client, srcURL, destPath string, max int64) (size int64, contentType, suffix string, err error) {
	// Cancelled by the stall timer below when the body stops arriving.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
	if err != nil {
		return 0, "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", "", fmt.Errorf("podcast: download %s: status %d", srcURL, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return 0, "", "", err
	}
	out, err := os.Create(destPath)
	if err != nil {
		return 0, "", "", err
	}
	defer out.Close()
	stall := time.AfterFunc(stallTimeout, cancel)
	defer stall.Stop()
	body := io.Reader(progressReader{r: resp.Body, onRead: func() { stall.Reset(stallTimeout) }})
	if max > 0 && max < math.MaxInt64 {
		// One byte past the limit is enough to tell "fits exactly" from "too big".
		// max+1 must not overflow: an "unlimited" ceiling arrives here as MaxInt64,
		// and a negative limit makes io.LimitReader hand back an empty body.
		body = io.LimitReader(body, max+1)
	}
	n, err := io.Copy(out, body)
	if err != nil {
		// Do not leave half an episode where a caller might take it for a whole one.
		_ = os.Remove(destPath)
		return 0, "", "", err
	}
	if max > 0 && n > max {
		// The partial file would otherwise sit on disk as an unplayable episode that
		// still fills the quota (status stays "error", so nothing points at it).
		_ = os.Remove(destPath)
		return 0, "", "", fmt.Errorf("podcast: %s exceeds the space left (%d bytes)", srcURL, max)
	}
	contentType = resp.Header.Get("Content-Type")
	// suffix is derived from destPath's extension (caller-chosen), not the remote resource.
	suffix = strings.TrimPrefix(filepath.Ext(destPath), ".")
	return n, contentType, suffix, nil
}

// progressReader calls onRead whenever bytes arrive, which is how a download tells
// "slow" from "stalled".
type progressReader struct {
	r      io.Reader
	onRead func()
}

func (p progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.onRead()
	}
	return n, err
}
