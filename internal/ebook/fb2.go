package ebook

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

// maxFB2Bytes caps how much of an FB2 document is read. The whole book, images
// included, is one XML file; the description comes first, so a bigger book still
// gets its metadata and only loses a cover stored past the cap. A var so tests
// can lower it.
var maxFB2Bytes int64 = 32 << 20

// parseFB2 reads metadata from a FictionBook 2 (.fb2) file.
// The FB2 format is a single XML document; all metadata lives under
// <description><title-info>.
func parseFB2(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()
	return parseFB2Reader(io.LimitReader(f, maxFB2Bytes))
}

// parseFB2Zip reads the first *.fb2 entry from a zip archive and parses it.
// The entry is streamed, never buffered whole, so a zip bomb costs at most
// maxFB2Bytes of decompression work.
func parseFB2Zip(path string) (Meta, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return Meta{}, err
	}
	defer zr.Close()

	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".fb2") {
			rc, err := f.Open()
			if err != nil {
				return Meta{}, err
			}
			defer rc.Close()
			return parseFB2Reader(io.LimitReader(rc, maxFB2Bytes))
		}
	}
	return Meta{}, errors.New("fb2: no .fb2 entry found in zip")
}

// --- FB2 XML structures ---

// fb2Description wraps <description>.
type fb2Description struct {
	TitleInfo fb2TitleInfo `xml:"title-info"`
}

// fb2TitleInfo maps <title-info> inside <description>.
type fb2TitleInfo struct {
	BookTitle  string      `xml:"book-title"`
	Authors    []fb2Author `xml:"author"`
	Lang       string      `xml:"lang"`
	Sequences  []fb2Seq    `xml:"sequence"`
	Genres     []string    `xml:"genre"`
	Annotation fb2Annot    `xml:"annotation"`
	Date       string      `xml:"date"`
	Coverpage  fb2Cover    `xml:"coverpage"`
}

// fb2Author maps a single <author> element.
// FB2 authors can have first/middle/last names or just a nickname.
type fb2Author struct {
	FirstName  string `xml:"first-name"`
	MiddleName string `xml:"middle-name"`
	LastName   string `xml:"last-name"`
	Nickname   string `xml:"nickname"`
}

// fb2Seq maps a <sequence> element (series name and number).
type fb2Seq struct {
	Name   string `xml:"name,attr"`
	Number string `xml:"number,attr"`
}

// fb2Annot is <annotation>; we capture its inner text via the chardata trick.
// Since annotation can contain child tags (<p>, <emphasis>, etc.) we accumulate
// all character data within the element.
type fb2Annot struct {
	Text string `xml:",innerxml"`
}

// fb2Cover maps <coverpage>.
type fb2Cover struct {
	Image fb2Image `xml:"image"`
}

// fb2Image maps <image> inside <coverpage>.
// The href is in the XLink namespace (xmlns:l="http://www.w3.org/1999/xlink").
// We bind both common prefixes (l: and xlink:) via the namespace URI.
type fb2Image struct {
	// XLink href attribute — must use the namespace URI, not the prefix.
	Href string `xml:"http://www.w3.org/1999/xlink href,attr"`
}

// parseFB2Bytes parses FB2 metadata from raw XML bytes.
func parseFB2Bytes(data []byte) (Meta, error) {
	return parseFB2Reader(bytes.NewReader(data))
}

// parseFB2Reader streams an FB2 document and keeps only the description and the
// cover binary; the text body and every other image are skipped, and reading
// stops once both are in hand. A document cut short after the description (by
// the read cap) still yields its metadata.
func parseFB2Reader(r io.Reader) (Meta, error) {
	dec := xml.NewDecoder(r)
	var (
		m       Meta
		coverID string
		gotDesc bool
		inRoot  bool
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			if gotDesc {
				return m, nil
			}
			return Meta{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !inRoot {
				inRoot = true // <FictionBook>
				continue
			}
			switch {
			case t.Name.Local == "description" && !gotDesc:
				var d fb2Description
				if err := dec.DecodeElement(&d, &t); err != nil {
					return Meta{}, err
				}
				m = fb2MetaFromTitleInfo(&d.TitleInfo)
				coverID = strings.TrimPrefix(d.TitleInfo.Coverpage.Image.Href, "#")
				gotDesc = true
				if coverID == "" {
					return m, nil
				}
			case t.Name.Local == "binary" && gotDesc && fb2Attr(t, "id") == coverID:
				if data, ok := fb2ReadCover(dec); ok && len(data) > 0 {
					m.CoverData = data
					m.CoverType = fb2Attr(t, "content-type")
				}
				return m, nil
			default:
				if err := dec.Skip(); err != nil {
					if gotDesc {
						return m, nil
					}
					return Meta{}, err
				}
			}
		case xml.EndElement: // </FictionBook>
			if gotDesc {
				return m, nil
			}
			return Meta{}, errors.New("fb2: no description")
		}
	}
}

func fb2Attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// fb2ReadCover decodes the base64 body of the cover <binary> straight from the
// decoder's buffer, without another copy of the encoded text. ok is false for a
// broken image or one that would decode to more than maxEbookCoverBytes.
func fb2ReadCover(dec *xml.Decoder) (data []byte, ok bool) {
	ok = true
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		switch t := tok.(type) {
		case xml.CharData:
			if !ok {
				continue
			}
			raw := bytes.TrimSpace(t)
			n := base64.StdEncoding.DecodedLen(len(raw))
			if int64(len(data)+n) > maxEbookCoverBytes {
				data, ok = nil, false
				continue
			}
			buf := make([]byte, n)
			w, err := base64.StdEncoding.Decode(buf, raw)
			if err != nil {
				data, ok = nil, false
				continue
			}
			data = append(data, buf[:w]...)
		case xml.StartElement:
			if err := dec.Skip(); err != nil {
				return nil, false
			}
		case xml.EndElement:
			return data, ok
		}
	}
}

// fb2MetaFromTitleInfo maps <title-info> to Meta (the cover is handled by the caller).
func fb2MetaFromTitleInfo(ti *fb2TitleInfo) Meta {
	var m Meta

	// Title.
	m.Title = strings.TrimSpace(ti.BookTitle)

	// Authors.
	for _, a := range ti.Authors {
		name, sortName := fb2AuthorNames(a)
		if name == "" {
			continue
		}
		m.Authors = append(m.Authors, Author{Name: name, SortName: sortName})
	}

	// Language.
	m.Language = strings.TrimSpace(ti.Lang)

	// Series: take the first <sequence> element.
	if len(ti.Sequences) > 0 {
		seq := ti.Sequences[0]
		m.Series = strings.TrimSpace(seq.Name)
		if n, err := strconv.ParseFloat(strings.TrimSpace(seq.Number), 64); err == nil {
			m.SeriesIndex = n
		}
	}

	// Tags from <genre>.
	for _, g := range ti.Genres {
		if v := strings.TrimSpace(g); v != "" {
			m.Tags = append(m.Tags, v)
		}
	}

	// Description: strip XML tags from annotation's inner XML.
	if raw := strings.TrimSpace(ti.Annotation.Text); raw != "" {
		m.Description = stripXMLTags(raw)
	}

	// Date.
	m.Date = strings.TrimSpace(ti.Date)

	return m
}

// fb2AuthorNames builds the display name and sort name for an FB2 author.
// If the author has a last name, sort order is "last first[middle]" (lowercased).
// If only a nickname is present, both Name and SortName derive from it.
func fb2AuthorNames(a fb2Author) (name, sortName string) {
	first := strings.TrimSpace(a.FirstName)
	middle := strings.TrimSpace(a.MiddleName)
	last := strings.TrimSpace(a.LastName)
	nick := strings.TrimSpace(a.Nickname)

	if first == "" && last == "" {
		// Nickname-only author.
		if nick == "" {
			return "", ""
		}
		return nick, strings.ToLower(nick)
	}

	// Build display name: "First [Middle] Last"
	parts := []string{}
	given := strings.TrimSpace(first + " " + middle)
	if given != "" {
		parts = append(parts, given)
	}
	if last != "" {
		parts = append(parts, last)
	}
	name = strings.Join(parts, " ")

	// Sort name: "last first[middle]" lowercased.
	sortParts := []string{}
	if last != "" {
		sortParts = append(sortParts, last)
	}
	if given != "" {
		sortParts = append(sortParts, given)
	}
	sortName = strings.ToLower(strings.Join(sortParts, " "))

	return name, sortName
}

// stripXMLTags removes XML/HTML tags from a string, leaving only text content.
func stripXMLTags(s string) string {
	// Decode the inner XML by re-parsing it as a fragment.
	// Wrap in a root element so xml.Decoder can process it.
	dec := xml.NewDecoder(strings.NewReader("<r>" + s + "</r>"))
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if cd, ok := tok.(xml.CharData); ok {
			b.Write(cd)
		}
	}
	return strings.TrimSpace(b.String())
}
