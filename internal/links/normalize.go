// Package links implements the "Share → DiscoDrive" inbox: links sent from phones,
// sorted through later in the web UI (open, archive, delete). Only the link and what
// helps recognise it are kept; see the package's Service for the background fetch.
package links

import (
	"errors"
	"net/url"
	"strings"
)

// ErrInvalidURL is returned for anything but an absolute http(s) link of sane length
// without credentials in it.
var ErrInvalidURL = errors.New("links: invalid url")

// MaxURLLen bounds a stored link (and the (user_id, url) index tuple).
const MaxURLLen = 2048

// trackingParams are dropped everywhere: they identify the sharer, not the content,
// and would turn one post shared twice into two inbox rows.
var trackingParams = map[string]bool{
	"igsh": true, "igshid": true, "si": true, "fbclid": true, "gclid": true,
	"share_id": true, "feature": true, "ref_src": true, "ref_url": true,
	"mibextid": true, "rdt": true, "_branch_match_id": true,
}

// xTrackingParams are only tracking on X/Twitter; elsewhere s and t mean something.
var xTrackingParams = map[string]bool{"s": true, "t": true}

// canonicalHosts folds mobile hosts into their desktop twin.
var canonicalHosts = map[string]string{
	"m.youtube.com":      "www.youtube.com",
	"mobile.twitter.com": "twitter.com",
	"mobile.x.com":       "x.com",
	"m.facebook.com":     "www.facebook.com",
	"m.reddit.com":       "www.reddit.com",
	"i.reddit.com":       "www.reddit.com",
}

// Normalize validates raw and returns the form links are stored and compared in:
// lower-case scheme and host, no fragment, no tracking parameters (the rest keep
// their order), mobile hosts folded.
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxURLLen {
		return "", ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", ErrInvalidURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrInvalidURL
	}
	u.Host = strings.ToLower(u.Host)
	if c, ok := canonicalHosts[u.Host]; ok {
		u.Host = c
	}
	u.Fragment, u.RawFragment = "", ""
	isX := SourceOf(u.Hostname()) == "x"
	if u.RawQuery != "" {
		kept := make([]string, 0, 4)
		for _, part := range strings.Split(u.RawQuery, "&") {
			if part == "" {
				continue
			}
			key, _, _ := strings.Cut(part, "=")
			if k, err := url.QueryUnescape(key); err == nil {
				key = k
			}
			key = strings.ToLower(key)
			if trackingParams[key] || strings.HasPrefix(key, "utm_") || (isX && xTrackingParams[key]) {
				continue
			}
			kept = append(kept, part)
		}
		u.RawQuery = strings.Join(kept, "&")
	}
	u.ForceQuery = false
	out := u.String()
	if len(out) > MaxURLLen {
		return "", ErrInvalidURL
	}
	return out, nil
}

// sourceDomains maps a registrable domain to the source shown in the UI.
var sourceDomains = map[string]string{
	"instagram.com": "instagram",
	"threads.net":   "threads",
	"threads.com":   "threads",
	"reddit.com":    "reddit",
	"redd.it":       "reddit",
	"youtube.com":   "youtube",
	"youtu.be":      "youtube",
	"twitter.com":   "x",
	"x.com":         "x",
	"tiktok.com":    "tiktok",
}

// SourceOf names where a link comes from, by its host: the domain itself or any of
// its subdomains. Everything else is "other".
func SourceOf(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for {
		if s, ok := sourceDomains[host]; ok {
			return s
		}
		_, rest, found := strings.Cut(host, ".")
		if !found || !strings.Contains(rest, ".") {
			return "other"
		}
		host = rest
	}
}
