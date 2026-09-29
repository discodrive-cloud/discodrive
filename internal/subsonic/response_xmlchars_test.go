package subsonic

import (
	"encoding/xml"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// Control characters in tags (a stray \x00 or \x1b from a tagger) must not make
// the whole XML response unparseable; tabs/newlines survive in attributes.
func TestWriteXMLDropsInvalidCharacters(t *testing.T) {
	rec := httptest.NewRecorder()
	writeXML(rec, "ok", map[string]any{
		"song": map[string]any{
			"title":   "Bad\x00Ti\x1btle\x7f",
			"comment": "line1\nline2\tx",
			"artist":  "broken \xff utf8 ￾",
		},
	})
	body := rec.Body.String()

	dec := xml.NewDecoder(strings.NewReader(body))
	var attrs = map[string]string{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("response is not well-formed XML: %v\n%q", err, body)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "song" {
			for _, a := range se.Attr {
				attrs[a.Name.Local] = a.Value
			}
		}
	}
	if attrs["title"] != "BadTitle\x7f" {
		t.Errorf("title = %q", attrs["title"])
	}
	if attrs["comment"] != "line1\nline2\tx" {
		t.Errorf("comment = %q, want newline and tab kept", attrs["comment"])
	}
	if attrs["artist"] != "broken � utf8 " {
		t.Errorf("artist = %q", attrs["artist"])
	}
}
