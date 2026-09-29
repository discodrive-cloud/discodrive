package subsonic

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

const (
	subsonicVersion = "1.16.1"
	serverType      = "discodrive"
	serverVersion   = "0.1.0"
	subsonicXMLNS   = "http://subsonic.org/restapi"
)

// writeOK writes a successful Subsonic response with the given payload.
// JSON merges the payload fields into the subsonic-response object; XML renders
// them as nested elements (Subsonic convention: scalars→attributes, objects/arrays→children).
func writeOK(w http.ResponseWriter, format string, payload any) {
	if format == "json" {
		writeJSON(w, baseEnvelope("ok"), payload)
		return
	}
	writeXML(w, "ok", payload)
}

// writeFail writes a Subsonic error response with the given code and message.
func writeFail(w http.ResponseWriter, format string, code int, msg string) {
	errPayload := map[string]any{"error": map[string]any{"code": code, "message": msg}}
	if format == "json" {
		writeJSON(w, baseEnvelope("failed"), errPayload)
		return
	}
	writeXML(w, "failed", errPayload)
}

func baseEnvelope(status string) map[string]any {
	return map[string]any{
		"status":        status,
		"version":       subsonicVersion,
		"type":          serverType,
		"serverVersion": serverVersion,
		"openSubsonic":  true,
	}
}

// writeJSON merges base and payload into the subsonic-response envelope and writes JSON.
func writeJSON(w http.ResponseWriter, base map[string]any, payload any) {
	merged := make(map[string]any, len(base)+4)
	for k, v := range base {
		merged[k] = v
	}
	switch p := payload.(type) {
	case map[string]any:
		for k, v := range p {
			merged[k] = v
		}
	default:
		if p != nil {
			b, err := json.Marshal(p)
			if err == nil {
				var m map[string]any
				if json.Unmarshal(b, &m) == nil {
					for k, v := range m {
						merged[k] = v
					}
				}
			}
		}
	}
	envelope := map[string]any{"subsonic-response": merged}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(envelope)
}

// writeXML serializes the payload to Subsonic XML. Many real clients (Amperfy, etc.)
// speak XML, so the payload must be fully rendered — not just the envelope.
// The payload is normalized to canonical map[string]any/[]any/scalars via a JSON
// round-trip so every concrete numeric/slice/map type is handled uniformly.
func writeXML(w http.ResponseWriter, status string, payload any) {
	var canon map[string]any
	if payload != nil {
		if b, err := json.Marshal(payload); err == nil {
			_ = json.Unmarshal(b, &canon)
		}
	}

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<subsonic-response xmlns="` + subsonicXMLNS + `" status="` + status +
		`" version="` + subsonicVersion + `" type="` + serverType +
		`" serverVersion="` + serverVersion + `" openSubsonic="true"`)

	attrs, children := splitAttrsChildren(canon)
	for _, k := range attrs {
		sb.WriteString(` ` + k + `="` + xmlEscape(scalarString(canon[k])) + `"`)
	}
	if len(children) == 0 {
		sb.WriteString(`></subsonic-response>`)
	} else {
		sb.WriteString(`>`)
		for _, k := range children {
			writeXMLChild(&sb, k, canon[k])
		}
		sb.WriteString(`</subsonic-response>`)
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(sb.String()))
}

// writeXMLChild renders a payload value under the element name `name`.
// A slice repeats the element; a map becomes an element with attrs+children;
// a scalar becomes an element with text content.
func writeXMLChild(sb *strings.Builder, name string, v any) {
	switch val := v.(type) {
	case map[string]any:
		writeXMLObject(sb, name, val)
	case []any:
		for _, item := range val {
			writeXMLChild(sb, name, item)
		}
	default:
		sb.WriteString("<" + name + ">" + xmlEscape(scalarString(v)) + "</" + name + ">")
	}
}

// writeXMLObject writes one object element: scalar fields as attributes, nested
// objects/arrays as child elements.
func writeXMLObject(sb *strings.Builder, name string, m map[string]any) {
	attrs, children := splitAttrsChildren(m)
	sb.WriteString("<" + name)
	for _, k := range attrs {
		sb.WriteString(` ` + k + `="` + xmlEscape(scalarString(m[k])) + `"`)
	}
	if len(children) == 0 {
		sb.WriteString("/>")
		return
	}
	sb.WriteString(">")
	for _, k := range children {
		writeXMLChild(sb, k, m[k])
	}
	sb.WriteString("</" + name + ">")
}

// splitAttrsChildren splits a normalized map into scalar keys (attributes) and
// complex keys (child elements), each sorted for stable output.
func splitAttrsChildren(m map[string]any) (attrs, children []string) {
	for k, v := range m {
		switch v.(type) {
		case map[string]any, []any:
			children = append(children, k)
		default:
			attrs = append(attrs, k)
		}
	}
	sort.Strings(attrs)
	sort.Strings(children)
	return attrs, children
}

// scalarString formats a JSON-normalized scalar (string/bool/float64/nil) as text.
// Integral floats render without a decimal point (songCount=8, not 8.0).
func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return ""
	}
}

// xmlEscape escapes a string for use in both attribute and text contexts.
// Characters XML 1.0 cannot carry at all (control characters from tags, lone
// surrogates, U+FFFE/U+FFFF) are dropped, invalid UTF-8 becomes U+FFFD, and
// tab/newline/CR are written as character references so attribute values keep
// them. One bad tag must not make the whole response unparseable.
func xmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\'':
			b.WriteString("&apos;")
		case r == '\t':
			b.WriteString("&#x9;")
		case r == '\n':
			b.WriteString("&#xA;")
		case r == '\r':
			b.WriteString("&#xD;")
		case r < 0x20, r >= 0xD800 && r <= 0xDFFF, r == 0xFFFE, r == 0xFFFF:
			// not an XML 1.0 Char: drop
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
