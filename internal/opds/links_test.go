package opds

import (
	"encoding/json"
	"encoding/xml"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCredentialLinksAllFormats(t *testing.T) {
	r := httptest.NewRequest("GET", "/opds?apiKey=a%2Bb%26c", nil)
	for _, href := range []string{"/opds", "/opds/all?page=2", "/opds/download/id", "/opds/cover/id", "/opds/search?q={searchTerms}"} {
		u, err := url.Parse(authenticatedLink(r, href))
		if err != nil {
			t.Fatal(err)
		}
		if u.Query().Get("apiKey") != "a+b&c" {
			t.Fatal(u)
		}
	}
	if got := authenticatedLink(r, "/opds/search{?query}"); got != "/opds/search?apiKey=a%2Bb%26c{&query}" {
		t.Fatal(got)
	}
	for _, href := range []string{"https://example.org/", "//example.org/opds", "/other"} {
		if authenticatedLink(r, href) != href {
			t.Fatal("leaked key")
		}
	}
	w := httptest.NewRecorder()
	writeAtomFeed(w, r, mediaTypeAcquisition, atomFeed{Links: []atomLink{{Href: "/opds"}}, Entries: []atomEntry{{Links: []atomLink{{Href: "/opds/download/id"}}}}})
	var atom atomFeed
	if err := xml.Unmarshal(w.Body.Bytes(), &atom); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(atom.Entries[0].Links[0].Href, "apiKey=") {
		t.Fatal(atom)
	}
	w = httptest.NewRecorder()
	writeJSONFeed(w, r, feed2{Groups: []group2{{Publications: []publication2{{Links: []link2{{Href: "/opds/download/id"}}, Images: []link2{{Href: "/opds/cover/id"}}}}}}})
	var f feed2
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.Groups[0].Publications[0].Images[0].Href, "apiKey=") {
		t.Fatal(f)
	}
	w = httptest.NewRecorder()
	(&Handler{}).searchDescription(w, r)
	var doc openSearchDescription
	if err := xml.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Urls[0].Template, "apiKey=") || !strings.Contains(doc.Urls[0].Template, "{searchTerms}") {
		t.Fatal(doc)
	}
}
