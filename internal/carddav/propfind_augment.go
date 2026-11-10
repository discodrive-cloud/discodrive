package carddav

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/beevik/etree"
)

// go-webdav does not return getctag and supported-report-set (it puts them in a 404 propstat).
// macOS Contacts (AddressBookCore) will not start syncing a collection without them: it needs
// the CTag anchor and must see addressbook-multiget support advertised. We intercept PROPFIND,
// pass it through go-webdav into a buffer, and inject these properties for address book collections
// (only when the client requested them — i.e. they appeared in the 404 propstat). getctag is taken
// from the address book's ctag counter.

// bufResponseWriter buffers the go-webdav response so the PROPFIND body can be rewritten.
type bufResponseWriter struct {
	header http.Header
	status int
	body   strings.Builder
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
	return b.body.WriteString(string(p))
}

// servePropfind passes PROPFIND through go-webdav and rewrites the body to inject getctag /
// supported-report-set for address book collections.
func (b *Backend) servePropfind(w http.ResponseWriter, r *http.Request, dav http.Handler) {
	if raw, err := io.ReadAll(r.Body); err == nil {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if headsOnlyPropfind(raw, "address-data") {
			r = r.WithContext(context.WithValue(r.Context(), headsOnlyKey{}, true))
		}
	}
	buf := newBufRW()
	dav.ServeHTTP(buf, r)
	body := []byte(buf.body.String())
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
			continue // address book collections only (not principal/home-set/object)
		}
		wantCtag, wantReports := false, false
		for _, ps := range resp.SelectElements("propstat") {
			status := ps.SelectElement("status")
			if status == nil || !strings.Contains(status.Text(), "404") {
				continue
			}
			prop := ps.SelectElement("prop")
			if prop == nil {
				continue
			}
			if el := prop.SelectElement("getctag"); el != nil {
				wantCtag = true
				prop.RemoveChild(el)
			}
			if el := prop.SelectElement("supported-report-set"); el != nil {
				wantReports = true
				prop.RemoveChild(el)
			}
			if len(prop.ChildElements()) == 0 {
				resp.RemoveChild(ps)
			}
		}
		if !wantCtag && !wantReports {
			continue
		}
		ctag := int64(0)
		if ab, err := b.svc.AddressbookByURI(ctx, uri); err == nil {
			ctag = ab.Ctag
		}
		ps := resp.CreateElement("propstat")
		prop := ps.CreateElement("prop")
		if wantCtag {
			g := prop.CreateElement("getctag")
			g.CreateAttr("xmlns", "http://calendarserver.org/ns/")
			g.SetText(strconv.FormatInt(ctag, 10))
		}
		if wantReports {
			// only reports actually implemented by go-webdav (sync-collection is NOT implemented — do not advertise it)
			srs := prop.CreateElement("supported-report-set")
			for _, rep := range []string{"addressbook-multiget", "addressbook-query"} {
				e := srs.CreateElement("supported-report").CreateElement("report").CreateElement(rep)
				e.CreateAttr("xmlns", "urn:ietf:params:xml:ns:carddav")
			}
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

// headsOnlyKey marks a PROPFIND that names its properties and does not ask for the cards'
// data: listing an address book for it reads uid, ETag, date and size only.
type headsOnlyKey struct{}

func headsOnly(ctx context.Context) bool {
	v, _ := ctx.Value(headsOnlyKey{}).(bool)
	return v
}

// headsOnlyPropfind reports whether a PROPFIND body lists the properties it wants and the
// data property is not among them. An empty body means allprop (RFC 4918), which is.
func headsOnlyPropfind(body []byte, dataProp string) bool {
	return bytes.Contains(body, []byte("prop")) &&
		!bytes.Contains(body, []byte("allprop")) && !bytes.Contains(body, []byte(dataProp))
}
