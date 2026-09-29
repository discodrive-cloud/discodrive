package caldav

import (
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"

	"discodrive/internal/db"
)

// macOS/iOS send PROPPATCH on a collection (calendar-color, calendar-order, displayname, etc.).
// go-webdav unconditionally responds to PROPPATCH with 501, and there is no way to override it
// via caldav.Backend. We intercept PROPPATCH ourselves: respond with 207 (acknowledging the
// properties) and PERSIST displayname (Apple creates a list with a placeholder, then PROPPATCHes
// the real name — this is how it gets saved), calendar-color and calendar-order (read back via
// propfind_augment.go).

type ppName struct{ XMLName xml.Name }

type ppProp struct {
	DisplayName *string  `xml:"DAV: displayname"`
	Color       *string  `xml:"http://apple.com/ns/ical/ calendar-color"`
	Order       *string  `xml:"http://apple.com/ns/ical/ calendar-order"`
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

// HandleProppatch responds with 207 and persists displayname / calendar-color / calendar-order.
func (b *Backend) HandleProppatch(w http.ResponseWriter, r *http.Request) {
	var upd ppUpdate
	_ = xml.NewDecoder(r.Body).Decode(&upd)

	var names []ppName
	var newName, newColor string
	var newOrder *int32
	hasName, hasColor, hasOrder := false, false, false
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
		if op.Prop.Order != nil {
			hasOrder = true
			if n, err := strconv.ParseInt(strings.TrimSpace(*op.Prop.Order), 10, 32); err == nil {
				o := int32(n)
				newOrder = &o
			}
		}
	}
	for _, op := range upd.Remove {
		names = append(names, op.Prop.Props...)
	}

	// A calendar shared with the caller is the owner's: the Set* queries are scoped to the
	// owner, so nothing a sharee sends is saved. Renaming is refused with 403 (RFC 4918
	// §9.2: all or nothing) so the client does not show a name the server never took.
	// Color and order are different: Apple sends calendar-order (and color) for every
	// calendar in the sidebar, shared ones included, whenever the user drags one; a 403
	// there surfaces as an account error. They are cosmetic and client-local, so a
	// sharee's color/order still gets 200 without being stored, as before.
	status := "HTTP/1.1 200 OK"
	sharee := false
	if _, uri, obj := parsePath(r.URL.Path); uri != "" && obj == "" {
		if cal, err := b.resolveCalendar(r.Context(), uri); err == nil && db.UUIDString(cal.UserID) != userID(r.Context()) {
			sharee = true
			if hasName {
				status = "HTTP/1.1 403 Forbidden"
			}
		}
	}

	// persist displayname / color / order on the collection (if PROPPATCH targets a calendar/list)
	if !sharee && ((hasName && newName != "") || newColor != "" || newOrder != nil) {
		if _, uri, obj := parsePath(r.URL.Path); uri != "" && obj == "" {
			if cal, err := b.resolveCalendar(r.Context(), uri); err == nil {
				calID := db.UUIDString(cal.ID)
				if hasName && newName != "" {
					_ = b.svc.SetCalendarName(r.Context(), userID(r.Context()), calID, newName)
				}
				if newColor != "" && newColor != cal.Color {
					_ = b.svc.SetCalendarColor(r.Context(), userID(r.Context()), calID, newColor)
				}
				if newOrder != nil && (!cal.SortOrder.Valid || cal.SortOrder.Int32 != *newOrder) {
					_ = b.svc.SetCalendarOrder(r.Context(), userID(r.Context()), calID, *newOrder)
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
	if hasOrder {
		sb.WriteString(`<calendar-order xmlns="http://apple.com/ns/ical/"/>`)
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
