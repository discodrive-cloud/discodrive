package podcast

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"discodrive/internal/safecontent"
)

// streamClient proxies on-demand episode audio. Unlike guardedClient it has no
// overall Timeout: a proxied episode streams at playback pace for its full
// duration, so a whole-exchange deadline would truncate playback. A
// ResponseHeaderTimeout still bounds the header phase, and CheckRedirect keeps
// the SSRF revalidation on every redirect target.
var streamClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error { return ValidateURL(req.URL.String()) },
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           safeDialer.DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// ProxyStream fetches srcURL (SSRF-guarded) and copies the response to w,
// forwarding the client's Range header and relaying status + audio headers so
// seeking works. For episodes not downloaded to disk.
//
// committed reports whether the response was already committed to the client
// (headers written). When committed is false, an error means nothing was
// written and the caller may safely emit its own error response. When committed
// is true, a partial stream already reached the client and the caller must NOT
// write again.
func ProxyStream(ctx context.Context, w http.ResponseWriter, r *http.Request, srcURL string) (committed bool, err error) {
	if err := ValidateURL(srcURL); err != nil {
		return false, err
	}
	return ProxyStreamUnsafe(ctx, streamClient, w, r, srcURL)
}

// ProxyStreamUnsafe is ProxyStream without the SSRF guard, using the given client. Tests only.
func ProxyStreamUnsafe(ctx context.Context, client *http.Client, w http.ResponseWriter, r *http.Request, srcURL string) (committed bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
	if err != nil {
		return false, err
	}
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("podcast: proxy %s: status %d", srcURL, resp.StatusCode)
	}

	ct, ok := safecontent.Media(resp.Header.Get("Content-Type"))
	if !ok {
		return false, fmt.Errorf("podcast: unsupported media type")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", ct)
	for _, h := range []string{"Content-Length", "Accept-Ranges", "Content-Range"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	committed = true
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return committed, err
}
