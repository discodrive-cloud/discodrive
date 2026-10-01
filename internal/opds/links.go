package opds

import (
	"net/http"
	"net/url"
	"strings"
)

// Preserve query credentials only on catalog-local links. Keep URI templates intact.
func authenticatedLink(r *http.Request, href string) string {
	key := r.URL.Query().Get("apiKey")
	if key == "" || !(href == "/opds" || strings.HasPrefix(href, "/opds/") || strings.HasPrefix(href, "/opds?")) {
		return href
	}
	if strings.Contains(href, "{?query}") {
		return strings.Replace(href, "{?query}", "?apiKey="+url.QueryEscape(key)+"{&query}", 1)
	}
	sep := "?"
	if strings.Contains(href, "?") {
		sep = "&"
	}
	return href + sep + "apiKey=" + url.QueryEscape(key)
}
