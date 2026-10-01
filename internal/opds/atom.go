package opds

import (
	"encoding/xml"
	"net/http"
)

// OPDS media type constants (OPDS 1.2 / RFC 5023).
const (
	mediaTypeNavigation  = "application/atom+xml;profile=opds-catalog;kind=navigation"
	mediaTypeAcquisition = "application/atom+xml;profile=opds-catalog;kind=acquisition"
)

// atomFeed is the root element of an OPDS Atom feed.
// The namespace attributes are emitted as XML attributes so clients receive the
// correct Atom and OPDS namespaces without requiring xml.Name tricks.
type atomFeed struct {
	XMLName   xml.Name    `xml:"feed"`
	Xmlns     string      `xml:"xmlns,attr"`
	XmlnsOPDS string      `xml:"xmlns:opds,attr"`
	ID        string      `xml:"id"`
	Title     string      `xml:"title"`
	Updated   string      `xml:"updated"` // RFC3339
	Links     []atomLink  `xml:"link"`
	Entries   []atomEntry `xml:"entry"`
}

// atomLink represents a link element inside a feed or entry.
type atomLink struct {
	Rel   string `xml:"rel,attr"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr,omitempty"`
}

// atomEntry is a single navigation or acquisition entry inside a feed.
type atomEntry struct {
	ID      string       `xml:"id"`
	Title   string       `xml:"title"`
	Updated string       `xml:"updated"` // RFC3339
	Content *atomContent `xml:"content,omitempty"`
	Authors []atomAuthor `xml:"author"`
	Links   []atomLink   `xml:"link"`
}

// atomAuthor is an Atom author element carrying a single name.
type atomAuthor struct {
	Name string `xml:"name"`
}

// atomContent holds the inline text description for a navigation entry.
type atomContent struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// writeAtomFeed sets Content-Type and writes the XML declaration + marshalled feed to w.
func writeAtomFeed(w http.ResponseWriter, r *http.Request, mediaType string, feed atomFeed) {
	for i := range feed.Links {
		feed.Links[i].Href = authenticatedLink(r, feed.Links[i].Href)
	}
	for i := range feed.Entries {
		for j := range feed.Entries[i].Links {
			feed.Entries[i].Links[j].Href = authenticatedLink(r, feed.Entries[i].Links[j].Href)
		}
	}
	// Ensure both Atom namespace attrs are always populated.
	feed.Xmlns = "http://www.w3.org/2005/Atom"
	feed.XmlnsOPDS = "http://opds-spec.org/2010/catalog"

	w.Header().Set("Content-Type", mediaType+"; charset=utf-8")

	data, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		http.Error(w, "opds: failed to marshal feed", http.StatusInternalServerError)
		return
	}

	w.Write([]byte(xml.Header))
	w.Write(data)
}
