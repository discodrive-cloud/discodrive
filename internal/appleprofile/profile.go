// Package appleprofile builds manually installed Apple DAV account profiles.
package appleprofile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type Options struct {
	ServerURL, UserID, Email, InstallationID string
	Calendars, Contacts                      bool
}

// Build only accepts HTTPS endpoints and deliberately omits passwords. Apple asks
// for the app password during installation; credentials do not enter download URLs
// or an unencrypted configuration profile.
func Build(o Options) ([]byte, error) {
	return build(o, "")
}

// Password-bearing output must stay in memory and be encrypted before delivery.
func build(o Options, password string) ([]byte, error) {
	u, err := url.Parse(o.ServerURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("an HTTPS server origin is required")
	}
	if (!o.Calendars && !o.Contacts) || o.UserID == "" || o.Email == "" || o.InstallationID == "" {
		return nil, errors.New("incomplete profile")
	}
	port := 443
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("invalid port")
		}
	}
	sum := sha256.Sum256([]byte(strings.TrimRight(o.ServerURL, "/") + "\n" + o.UserID + "\n" + o.InstallationID))
	identifier := "org.discodrive.dav." + hex.EncodeToString(sum[:16])
	var b bytes.Buffer
	b.WriteString(xml.Header + `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict>`)
	text := func(k, v string) {
		b.WriteString("<key>" + k + "</key><string>")
		_ = xml.EscapeText(&b, []byte(v))
		b.WriteString("</string>")
	}
	integer := func(k string, v int) { fmt.Fprintf(&b, "<key>%s</key><integer>%d</integer>", k, v) }
	payload := func(id, kind, name string) {
		text("PayloadIdentifier", id)
		text("PayloadUUID", uuid(id))
		text("PayloadType", kind)
		integer("PayloadVersion", 1)
		text("PayloadDisplayName", name)
	}
	payload(identifier, "Configuration", "DiscoDrive")
	text("PayloadScope", "User")
	b.WriteString("<key>PayloadContent</key><array>")
	for _, item := range []struct {
		enabled         bool
		key, kind, path string
	}{
		{o.Calendars, "CalDAV", "caldav", "caldav"}, {o.Contacts, "CardDAV", "carddav", "carddav"},
	} {
		if !item.enabled {
			continue
		}
		b.WriteString("<dict>")
		payload(identifier+"."+item.kind, "com.apple."+item.kind+".account", "DiscoDrive")
		text(item.key+"AccountDescription", "DiscoDrive ("+u.Host+")")
		text(item.key+"HostName", u.Hostname())
		integer(item.key+"Port", port)
		text(item.key+"PrincipalURL", strings.TrimRight(o.ServerURL, "/")+"/"+item.path+"/"+url.PathEscape(o.UserID)+"/")
		text(item.key+"Username", o.Email)
		if password != "" {
			text(item.key+"Password", password)
		}
		b.WriteString("<key>" + item.key + "UseSSL</key><true/></dict>")
	}
	b.WriteString("</array></dict></plist>")
	return b.Bytes(), nil
}

func uuid(seed string) string {
	s := sha256.Sum256([]byte(seed))
	s[6] = (s[6] & 0x0f) | 0x50
	s[8] = (s[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", s[:4], s[4:6], s[6:8], s[8:10], s[10:16])
}
