package opds

import (
	"encoding/json"
	"net/http"
)

// mediaTypeOPDS2 is the OPDS 2.0 media type (Readium Web Publication Manifest).
const mediaTypeOPDS2 = "application/opds+json"

// feed2 is the top-level OPDS 2.0 navigation/acquisition feed object.
type feed2 struct {
	Metadata     metadata2      `json:"metadata"`
	Links        []link2        `json:"links,omitempty"`
	Navigation   []link2        `json:"navigation,omitempty"`
	Groups       []group2       `json:"groups,omitempty"`
	Publications []publication2 `json:"publications,omitempty"`
}

// metadata2 carries the feed title and optional @type.
type metadata2 struct {
	Type  string `json:"@type,omitempty"`
	Title string `json:"title"`
}

// link2 represents an OPDS 2.0 link object.
type link2 struct {
	Href      string `json:"href"`
	Type      string `json:"type,omitempty"`
	Rel       string `json:"rel,omitempty"`
	Title     string `json:"title,omitempty"`
	Templated bool   `json:"templated,omitempty"`
}

// group2 is an OPDS 2.0 group inside a feed.
type group2 struct {
	Metadata     metadata2      `json:"metadata"`
	Links        []link2        `json:"links,omitempty"`
	Navigation   []link2        `json:"navigation,omitempty"`
	Publications []publication2 `json:"publications,omitempty"`
}

// publication2 is a minimal OPDS 2.0 publication object (populated by Task 13).
type publication2 struct {
	Metadata pubMetadata2 `json:"metadata"`
	Links    []link2      `json:"links,omitempty"`
	Images   []link2      `json:"images,omitempty"`
}

// pubMetadata2 carries per-publication metadata for acquisition feeds.
type pubMetadata2 struct {
	Title      string       `json:"title"`
	Author     []pubAuthor2 `json:"author,omitempty"`
	Language   string       `json:"language,omitempty"`
	Identifier string       `json:"identifier,omitempty"`
}

// pubAuthor2 is an OPDS 2.0 contributor object (we only carry the name).
type pubAuthor2 struct {
	Name string `json:"name"`
}

// writeJSONFeed encodes feed as OPDS 2.0 JSON and writes it to w.
func writeJSONFeed(w http.ResponseWriter, r *http.Request, feed feed2) {
	links := func(ls []link2) {
		for i := range ls {
			ls[i].Href = authenticatedLink(r, ls[i].Href)
		}
	}
	pubs := func(ps []publication2) {
		for i := range ps {
			links(ps[i].Links)
			links(ps[i].Images)
		}
	}
	links(feed.Links)
	links(feed.Navigation)
	pubs(feed.Publications)
	for i := range feed.Groups {
		links(feed.Groups[i].Links)
		links(feed.Groups[i].Navigation)
		pubs(feed.Groups[i].Publications)
	}
	w.Header().Set("Content-Type", mediaTypeOPDS2+"; charset=utf-8")
	if err := json.NewEncoder(w).Encode(feed); err != nil {
		// Header already written; nothing useful we can do.
		return
	}
}
