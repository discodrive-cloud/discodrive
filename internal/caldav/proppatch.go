package caldav

import (
	"encoding/xml"
	"net/http"
	"strings"

	"discodrive/internal/db"
)

// macOS/iOS send PROPPATCH on a collection (calendar-color, calendar-order, displayname, etc.).
// go-webdav unconditionally responds to PROPPATCH with 501, and there is no way to override it
// via caldav.Backend. We intercept PROPPATCH ourselves: respond with 207 (acknowledging the
// properties) and PERSIST displayname (Apple creates a list with a placeholder, then PROPPATCHes
// the real name — this is how it gets saved) and calendar-color (read back via propfind_augment.go).

type ppName struct{ XMLName xml.Name }

type ppProp struct {
	DisplayName *string  `xml:"DAV: displayname"`
	Color       *string  `xml:"http://apple.com/ns/ical/ calendar-color"`
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

// HandleProppatch responds with 207 and persists displayname / calendar-color if provided.
func (b *Backend) HandleProppatch(w http.ResponseWriter, r *http.Request) {
	var upd ppUpdate
	_ = xml.NewDecoder(r.Body).Decode(&upd)

	var names []ppName
	var newName, newColor string
	hasName, hasColor := false, false
	for _, op := range upd.Set {
		names = append(names, op.Prop.Props...)
		if op.Prop.DisplayName != nil {
			hasName = true
			newName = strings.TrimSpace(*op.Prop.DisplayName)
		}
		if op.Prop.Color != nil {
			hasColor = true
			newColor = normalizeColor(*op.Prop.Color)
		}
	}
	for _, op := range upd.Remove {
		names = append(names, op.Prop.Props...)
	}

	// persist displayname / color on the collection (if PROPPATCH targets a calendar/list)
	if (hasName && newName != "") || newColor != "" {
		if _, uri, obj := parsePath(r.URL.Path); uri != "" && obj == "" {
			if cal, err := b.resolveCalendar(r.Context(), uri); err == nil {
				calID := db.UUIDString(cal.ID)
				if hasName && newName != "" {
					_ = b.svc.SetCalendarName(r.Context(), userID(r.Context()), calID, newName)
				}
				if newColor != "" && newColor != cal.Color {
					_ = b.svc.SetCalendarColor(r.Context(), userID(r.Context()), calID, newColor)
				}
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
	if hasColor {
		sb.WriteString(`<calendar-color xmlns="http://apple.com/ns/ical/"/>`)
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
	sb.WriteString(`</prop><status>HTTP/1.1 200 OK</status></propstat></response></multistatus>`)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(sb.String()))
}
