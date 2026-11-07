package links

import (
	"io"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// Limits on the strings taken from a page: they are shown in a one-line list row.
const (
	maxTitleRunes  = 300
	maxAuthorRunes = 100
)

// Meta is what a page says about itself in <head>. Image is the raw og:image value,
// possibly relative; the caller resolves it against the page URL.
type Meta struct {
	Title, Author, Image string
}

// parseHead reads <title> and the og:/twitter: meta tags. It stops at </head> or
// <body>: everything useful lives in <head>, and stopping early is what keeps a huge
// page from being read past what the caller's limit already allows. The values are
// untrusted text and come back cleaned and cut to length.
func parseHead(r io.Reader) Meta {
	var title, ogTitle, twTitle, ogImage, twImage, author string
	z := html.NewTokenizer(r)
	done := false
	for !done {
		switch z.Next() {
		case html.ErrorToken:
			done = true
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "head" {
				done = true
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			switch tok.Data {
			case "body":
				done = true
			case "title":
				if title == "" && z.Next() == html.TextToken {
					title = string(z.Text())
				}
			case "meta":
				var key, content string
				for _, a := range tok.Attr {
					switch a.Key {
					case "property", "name":
						if key == "" {
							key = strings.ToLower(strings.TrimSpace(a.Val))
						}
					case "content":
						content = a.Val
					}
				}
				set := func(dst *string) {
					if *dst == "" {
						*dst = content
					}
				}
				switch key {
				case "og:title":
					set(&ogTitle)
				case "twitter:title":
					set(&twTitle)
				case "og:image", "og:image:url", "og:image:secure_url":
					set(&ogImage)
				case "twitter:image", "twitter:image:src":
					set(&twImage)
				case "author", "article:author":
					set(&author)
				}
			}
		}
	}
	return Meta{
		Title:  cleanText(firstNonEmpty(ogTitle, twTitle, title), maxTitleRunes),
		Author: cleanText(author, maxAuthorRunes),
		Image:  strings.TrimSpace(firstNonEmpty(ogImage, twImage)),
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// cleanText drops control and invisible format characters (bidi overrides included,
// so a title cannot reorder the row around it), collapses whitespace and cuts to max runes.
func cleanText(s string, max int) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
			continue
		}
		if space {
			if n+1 >= max {
				break
			}
			b.WriteByte(' ')
			n++
			space = false
		}
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
