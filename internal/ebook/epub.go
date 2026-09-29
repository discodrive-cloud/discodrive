package ebook

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"
)

// parseEPUB reads metadata from an EPUB file (stdlib only: archive/zip + encoding/xml).
// Flow: open zip → META-INF/container.xml → OPF rootfile → parse <metadata>.
func parseEPUB(fpath string) (Meta, error) {
	zr, err := zip.OpenReader(fpath)
	if err != nil {
		return Meta{}, err
	}
	defer zr.Close()

	// 1. Read META-INF/container.xml to find the OPF rootfile path.
	opfPath, err := epubRootfilePath(zr)
	if err != nil {
		return Meta{}, err
	}

	// 2. Parse the OPF document.
	opfData, err := epubReadFile(zr, opfPath, maxEbookMetaBytes)
	if err != nil {
		return Meta{}, err
	}

	var pkg opfPackage
	if err := xml.Unmarshal(opfData, &pkg); err != nil {
		return Meta{}, err
	}

	opfDir := path.Dir(opfPath)
	return epubBuildMeta(&pkg, zr, opfDir)
}

// epubRootfilePath extracts the OPF path from META-INF/container.xml.
func epubRootfilePath(zr *zip.ReadCloser) (string, error) {
	data, err := epubReadFile(zr, "META-INF/container.xml", maxEbookMetaBytes)
	if err != nil {
		return "", errors.New("epub: missing META-INF/container.xml")
	}

	var container struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(data, &container); err != nil {
		return "", err
	}
	if len(container.Rootfiles) == 0 || container.Rootfiles[0].FullPath == "" {
		return "", errors.New("epub: no rootfile found in container.xml")
	}
	return container.Rootfiles[0].FullPath, nil
}

// Caps on how much of a single zip entry is buffered. Books are untrusted uploads
// and are indexed automatically, so a zip bomb in one entry must not take the heap
// of a 128 MB box with it.
var (
	maxEbookMetaBytes  int64 = 4 << 20  // container.xml, OPF
	maxEbookCoverBytes int64 = 20 << 20 // cover image
)

var errZipEntryTooLarge = errors.New("ebook: zip entry exceeds size limit")

// readZipEntry reads f fully if it is at most limit bytes. The declared size is
// checked first; the LimitReader protects against a header that lies about it.
func readZipEntry(f *zip.File, limit int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(limit) {
		return nil, errZipEntryTooLarge
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errZipEntryTooLarge
	}
	return data, nil
}

// epubReadFile reads a named file of at most limit bytes from the zip.
func epubReadFile(zr *zip.ReadCloser, name string, limit int64) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			return readZipEntry(f, limit)
		}
	}
	return nil, errors.New("epub: file not found in zip: " + name)
}

// --- OPF XML structures ---

// opfPackage maps the root <package> element of an OPF document.
// We use the xml package's namespace-aware attribute matching via XMLName
// and struct tags that include namespace URIs.
type opfPackage struct {
	XMLName  xml.Name    `xml:"package"`
	Metadata opfMetadata `xml:"metadata"`
	Manifest opfManifest `xml:"manifest"`
}

const (
	nsDC  = "http://purl.org/dc/elements/1.1/"
	nsOPF = "http://www.idpf.org/2007/opf"
)

// opfMetadata holds all Dublin Core and OPF meta elements.
type opfMetadata struct {
	Titles       []opfDCText     `xml:"http://purl.org/dc/elements/1.1/ title"`
	Creators     []opfCreator    `xml:"http://purl.org/dc/elements/1.1/ creator"`
	Languages    []opfDCText     `xml:"http://purl.org/dc/elements/1.1/ language"`
	Identifiers  []opfIdentifier `xml:"http://purl.org/dc/elements/1.1/ identifier"`
	Publishers   []opfDCText     `xml:"http://purl.org/dc/elements/1.1/ publisher"`
	Dates        []opfDCText     `xml:"http://purl.org/dc/elements/1.1/ date"`
	Descriptions []opfDCText     `xml:"http://purl.org/dc/elements/1.1/ description"`
	Subjects     []opfDCText     `xml:"http://purl.org/dc/elements/1.1/ subject"`
	Metas        []opfMeta       `xml:"meta"`
}

// opfDCText is a simple Dublin Core text element.
type opfDCText struct {
	Value string `xml:",chardata"`
}

// opfCreator is dc:creator with optional opf:file-as and opf:role attributes.
// EPUB2 OPF uses opf: namespace for these attributes on DC elements.
type opfCreator struct {
	Value  string `xml:",chardata"`
	FileAs string `xml:"http://www.idpf.org/2007/opf file-as,attr"`
	Role   string `xml:"http://www.idpf.org/2007/opf role,attr"`
}

// opfIdentifier is dc:identifier with an optional scheme attribute.
type opfIdentifier struct {
	Value  string `xml:",chardata"`
	Scheme string `xml:"http://www.idpf.org/2007/opf scheme,attr"`
	ID     string `xml:"id,attr"`
}

// opfMeta represents <meta name="..." content="..."> (EPUB2) and
// <meta property="...">value</meta> (EPUB3).
type opfMeta struct {
	Name     string `xml:"name,attr"`
	Content  string `xml:"content,attr"`
	Property string `xml:"property,attr"`
	Refines  string `xml:"refines,attr"`
	Value    string `xml:",chardata"`
}

// opfManifest holds all <item> elements.
type opfManifest struct {
	Items []opfItem `xml:"item"`
}

// opfItem is a single manifest entry.
type opfItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

// --- Build Meta from parsed OPF ---

func epubBuildMeta(pkg *opfPackage, zr *zip.ReadCloser, opfDir string) (Meta, error) {
	md := &pkg.Metadata
	var m Meta

	// Title.
	if len(md.Titles) > 0 {
		m.Title = strings.TrimSpace(md.Titles[0].Value)
	}

	// Authors.
	for _, c := range md.Creators {
		name := strings.TrimSpace(c.Value)
		if name == "" {
			continue
		}
		sortName := strings.TrimSpace(c.FileAs)
		if sortName == "" {
			sortName = strings.ToLower(name)
		}
		m.Authors = append(m.Authors, Author{Name: name, SortName: sortName})
	}

	// Language.
	if len(md.Languages) > 0 {
		m.Language = strings.TrimSpace(md.Languages[0].Value)
	}

	// ISBN: pick first identifier that looks like an ISBN.
	for _, id := range md.Identifiers {
		v := strings.TrimSpace(id.Value)
		s := strings.ToUpper(strings.TrimSpace(id.Scheme))
		if s == "ISBN" || strings.HasPrefix(s, "ISBN") || isISBNLike(v) {
			m.ISBN = v
			break
		}
	}

	// Publisher.
	if len(md.Publishers) > 0 {
		m.Publisher = strings.TrimSpace(md.Publishers[0].Value)
	}

	// Date.
	if len(md.Dates) > 0 {
		m.Date = strings.TrimSpace(md.Dates[0].Value)
	}

	// Description.
	if len(md.Descriptions) > 0 {
		m.Description = strings.TrimSpace(md.Descriptions[0].Value)
	}

	// Tags (dc:subject).
	for _, s := range md.Subjects {
		if v := strings.TrimSpace(s.Value); v != "" {
			m.Tags = append(m.Tags, v)
		}
	}

	// Calibre series + index from EPUB2 <meta name="..."> elements.
	// Also handle EPUB3 belongs-to-collection.
	epubMetaMap := make(map[string]string)
	for _, meta := range md.Metas {
		if meta.Name != "" {
			epubMetaMap[meta.Name] = meta.Content
		}
	}
	if s, ok := epubMetaMap["calibre:series"]; ok {
		m.Series = s
	}
	if idx, ok := epubMetaMap["calibre:series_index"]; ok {
		if f, err := strconv.ParseFloat(idx, 64); err == nil {
			m.SeriesIndex = f
		}
	}
	// EPUB3 belongs-to-collection (property on <meta> elements).
	if m.Series == "" {
		for _, meta := range md.Metas {
			if meta.Property == "belongs-to-collection" && meta.Refines == "" {
				m.Series = strings.TrimSpace(meta.Value)
			}
		}
	}

	// Cover image.
	coverHref, coverType := epubFindCover(pkg, md)
	if coverHref != "" {
		// Resolve href relative to the OPF directory.
		coverPath := coverHref
		if opfDir != "." && opfDir != "" {
			coverPath = opfDir + "/" + coverHref
		}
		if data, err := epubReadFile(zr, coverPath, maxEbookCoverBytes); err == nil {
			m.CoverData = data
			m.CoverType = coverType
		}
	}

	return m, nil
}

// epubFindCover returns the href and media-type of the cover image.
// Priority: EPUB3 manifest item with properties="cover-image";
// fallback: EPUB2 <meta name="cover" content="ITEM_ID"> → manifest item with that id.
func epubFindCover(pkg *opfPackage, md *opfMetadata) (href, mediaType string) {
	// EPUB3: manifest item with properties containing "cover-image".
	for _, item := range pkg.Manifest.Items {
		for _, prop := range strings.Fields(item.Properties) {
			if prop == "cover-image" {
				return item.Href, item.MediaType
			}
		}
	}

	// EPUB2: <meta name="cover" content="ITEM_ID">.
	for _, meta := range md.Metas {
		if meta.Name == "cover" && meta.Content != "" {
			itemID := meta.Content
			for _, item := range pkg.Manifest.Items {
				if item.ID == itemID {
					return item.Href, item.MediaType
				}
			}
		}
	}

	return "", ""
}

// isISBNLike returns true if the value looks like an ISBN (digits, hyphens,
// optionally ending with X, length 10 or 13 after stripping hyphens).
func isISBNLike(v string) bool {
	stripped := strings.ReplaceAll(v, "-", "")
	stripped = strings.ReplaceAll(stripped, " ", "")
	n := len(stripped)
	if n != 10 && n != 13 {
		return false
	}
	for i, c := range stripped {
		if c >= '0' && c <= '9' {
			continue
		}
		// ISBN-10 may end with 'X'.
		if i == 9 && (c == 'X' || c == 'x') {
			continue
		}
		return false
	}
	return true
}
