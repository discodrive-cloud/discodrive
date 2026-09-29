package carddav

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/beevik/etree"

	"discodrive/internal/db"
)

// REPORT (addressbook-multiget/query) in go-webdav returns a RE-SERIALIZED vCard (decode→encode
// via go-vcard), which corrupts data (photos → truncated data-URIs, reordering of parameters)
// and accumulates corruption with each round of edits. We serve the raw vCard byte-for-byte here too.
// Technique: etree holds the multistatus structure; address-data is replaced with a placeholder,
// and after serialization the raw vCard is substituted back with manual XML escaping
// (CR → &#13; so that CRLF survives XML-parser normalization on the client).

func (b *Backend) serveReport(w http.ResponseWriter, r *http.Request, dav http.Handler) {
	buf := newBufRW()
	dav.ServeHTTP(buf, r)
	body := []byte(buf.body.String())
	if buf.status == http.StatusMultiStatus {
		body = b.augmentReport(r.Context(), body)
	}
	for k, v := range buf.header {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(buf.status)
	_, _ = w.Write(body)
}

var vcardXMLEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#13;")

func (b *Backend) augmentReport(ctx context.Context, body []byte) []byte {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(body); err != nil {
		return body
	}
	ms := doc.Root()
	if ms == nil || ms.Tag != "multistatus" {
		return body
	}
	var repl []string // placeholder, raw vCard, ... — for one strings.Replacer pass
	n := 0
	for _, resp := range ms.SelectElements("response") {
		href := resp.SelectElement("href")
		if href == nil {
			continue
		}
		// href is the escaped path (a UID with '@' or a space comes as %40 / %20); objects
		// are stored under the unescaped name, as go-webdav hands it to the backend.
		p, err := url.PathUnescape(href.Text())
		if err != nil {
			continue
		}
		_, uri, obj := parsePath(p)
		if uri == "" || obj == "" {
			continue
		}
		ad := resp.FindElement(".//address-data")
		if ad == nil {
			continue
		}
		ab, err := b.resolveAddressbook(ctx, uri)
		if err != nil {
			continue
		}
		data, _, err := b.svc.GetAddressbookObject(ctx, db.UUIDString(ab.ID), obj)
		if err != nil {
			continue
		}
		ph := fmt.Sprintf("__KF_RAWVCARD_%d__", n)
		n++
		ad.SetText(ph)
		repl = append(repl, ph, vcardXMLEscaper.Replace(data))
	}
	if n == 0 {
		return body
	}
	out, err := doc.WriteToString()
	if err != nil {
		return body
	}
	// One pass: substituted text is never scanned again, so a card that happens to contain
	// another placeholder's text cannot pull a different card into its place.
	return []byte(strings.NewReplacer(repl...).Replace(out))
}
