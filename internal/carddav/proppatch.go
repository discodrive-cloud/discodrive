package carddav

import (
	"encoding/xml"
	"net/http"
	"strings"

	"discodrive/internal/db"
)

// macOS/iOS send PROPPATCH on a collection (displayname, etc.).
// go-webdav unconditionally responds to PROPPATCH with 501, and there is no way to override it
// via carddav.Backend. We intercept PROPPATCH ourselves: respond with 207 (acknowledging the
// properties) and PERSIST displayname (Apple creates a list with a placeholder, then PROPPATCHes
// the real name — this is how it gets saved).

type ppName struct{ XMLName xml.Name }

type ppProp struct {
	DisplayName *string  `xml:"DAV: displayname"`
	Props       []ppName `xml:",any"`
}

type ppOp struct {
	Prop ppProp `xml:"DAV: prop"`
}

type ppUpdate struct {
	XMLName xml.Name `xml:"DAV: propertyupdate"`
	Set     []ppOp   `xml:"DAV: set"`
	Remove  []ppOp   `xml:"DAV: remove"`
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

// HandleProppatch responds with 207 (no-op acknowledgement) and persists displayname if provided.
func (b *Backend) HandleProppatch(w http.ResponseWriter, r *http.Request) {
	var upd ppUpdate
	_ = xml.NewDecoder(r.Body).Decode(&upd)

	var names []ppName
	var newName string
	hasName := false
	for _, op := range upd.Set {
		names = append(names, op.Prop.Props...)
		if op.Prop.DisplayName != nil {
			hasName = true
			newName = strings.TrimSpace(*op.Prop.DisplayName)
		}
	}
	for _, op := range upd.Remove {
		names = append(names, op.Prop.Props...)
	}

	// An address book shared with the caller is the owner's to rename: answer 403 rather than
	// acknowledging a change the owner-scoped query silently drops.
	status := "HTTP/1.1 200 OK"
	if _, uri, obj := parsePath(r.URL.Path); uri != "" && obj == "" {
		if ab, err := b.resolveAddressbook(r.Context(), uri); err == nil && db.UUIDString(ab.UserID) != userID(r.Context()) {
			status = "HTTP/1.1 403 Forbidden"
		}
	}

	// persist displayname on the collection (if PROPPATCH targets an address book)
	if status == "HTTP/1.1 200 OK" && hasName && newName != "" {
		if _, uri, obj := parsePath(r.URL.Path); uri != "" && obj == "" {
			if ab, err := b.resolveAddressbook(r.Context(), uri); err == nil {
				_ = b.svc.SetAddressbookName(r.Context(), userID(r.Context()), db.UUIDString(ab.ID), newName)
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<multistatus xmlns="DAV:"><response><href>`)
	sb.WriteString(xmlEscaper.Replace(r.URL.Path))
	sb.WriteString(`</href><propstat><prop>`)
	if hasName {
		sb.WriteString(`<displayname/>`)
	}
	for _, n := range names {
		sb.WriteString("<")
		sb.WriteString(n.XMLName.Local)
		if n.XMLName.Space != "" {
			sb.WriteString(` xmlns="`)
			sb.WriteString(xmlEscaper.Replace(n.XMLName.Space))
			sb.WriteString(`"`)
		}
		sb.WriteString("/>")
	}
	sb.WriteString(`</prop><status>` + status + `</status></propstat></response></multistatus>`)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(sb.String()))
}
