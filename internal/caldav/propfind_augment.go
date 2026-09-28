package caldav

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/beevik/etree"
)

// go-webdav does not know Apple's calendar-color and calendar-order properties: they land in a
// 404 propstat, and macOS Calendar re-sends them via PROPPATCH on every sync. We intercept
// PROPFIND, pass it through go-webdav into a buffer, and inject the stored values for calendar
// collections (only when the client requested them).

const appleICalNS = "http://apple.com/ns/ical/"

// bufResponseWriter buffers the go-webdav response so the PROPFIND body can be rewritten.
type bufResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func newBufRW() *bufResponseWriter               { return &bufResponseWriter{header: http.Header{}, status: 200} }
func (b *bufResponseWriter) Header() http.Header { return b.header }
func (b *bufResponseWriter) WriteHeader(s int) {
	if !b.wrote {
		b.status = s
		b.wrote = true
	}
}
func (b *bufResponseWriter) Write(p []byte) (int, error) {
	b.wrote = true
	return b.body.Write(p)
}

// HandlePropfind passes PROPFIND through go-webdav and injects calendar-color / calendar-order.
// Requests that ask for neither are streamed through untouched.
func (b *Backend) HandlePropfind(w http.ResponseWriter, r *http.Request, dav http.Handler) {
	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if !bytes.Contains(raw, []byte("calendar-color")) && !bytes.Contains(raw, []byte("calendar-order")) {
		dav.ServeHTTP(w, r)
		return
	}
	buf := newBufRW()
	dav.ServeHTTP(buf, r)
	body := buf.body.Bytes()
	if buf.status == http.StatusMultiStatus {
		body = b.augmentPropfind(r.Context(), body)
	}
	for k, v := range buf.header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(buf.status)
	_, _ = w.Write(body)
}

func (b *Backend) augmentPropfind(ctx context.Context, body []byte) []byte {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(body); err != nil {
		return body
	}
	ms := doc.Root()
	if ms == nil || ms.Tag != "multistatus" {
		return body
	}
	changed := false
	for _, resp := range ms.SelectElements("response") {
		href := resp.SelectElement("href")
		if href == nil {
			continue
		}
		_, uri, obj := parsePath(href.Text())
		if uri == "" || obj != "" {
			continue // calendar collections only (not principal/home-set/object)
		}
		cal, err := b.resolveCalendar(ctx, uri)
		if err != nil {
			continue
		}
		// only stored values are injected: an unset one keeps its 404, so the client pushes its own
		stored := map[string]string{}
		if cal.Color != "" {
			stored["calendar-color"] = appleColor(cal.Color)
		}
		if cal.SortOrder.Valid {
			stored["calendar-order"] = strconv.Itoa(int(cal.SortOrder.Int32))
		}
		var found []string
		for _, ps := range resp.SelectElements("propstat") {
			status := ps.SelectElement("status")
			if status == nil || !strings.Contains(status.Text(), "404") {
				continue
			}
			prop := ps.SelectElement("prop")
			if prop == nil {
				continue
			}
			for _, el := range prop.ChildElements() {
				if _, ok := stored[el.Tag]; ok && el.NamespaceURI() == appleICalNS {
					found = append(found, el.Tag)
					prop.RemoveChild(el)
				}
			}
			if len(prop.ChildElements()) == 0 {
				resp.RemoveChild(ps)
			}
		}
		if len(found) == 0 {
			continue
		}
		ps := resp.CreateElement("propstat")
		prop := ps.CreateElement("prop")
		for _, tag := range found {
			el := prop.CreateElement(tag)
			el.CreateAttr("xmlns", appleICalNS)
			el.SetText(stored[tag])
		}
		ps.CreateElement("status").SetText("HTTP/1.1 200 OK")
		changed = true
	}
	if !changed {
		return body
	}
	out, err := doc.WriteToBytes()
	if err != nil {
		return body
	}
	return out
}

// normalizeColor turns a client color (#RRGGBB or Apple's #RRGGBBAA) into the stored #rrggbb
// form used by the web UI. Returns "" for anything else.
func normalizeColor(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 7 && len(s) != 9 || s[0] != '#' {
		return ""
	}
	for _, ch := range s[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", ch) {
			return ""
		}
	}
	return strings.ToLower(s[:7])
}

// appleColor renders a stored #rrggbb color in Apple's #RRGGBBAA form (opaque).
func appleColor(stored string) string {
	if len(stored) == 7 {
		return stored + "FF"
	}
	return stored
}
