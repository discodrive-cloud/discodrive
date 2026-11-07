package links

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseHeadPriorities(t *testing.T) {
	m := parseHead(strings.NewReader(`<html><head>
<title>Plain title</title>
<meta name="twitter:title" content="Twitter title">
<meta property="og:title" content="OG &amp; title">
<meta name="twitter:image" content="https://cdn/t.jpg">
<meta property="og:image" content="/img/o.jpg">
<meta name="author" content="@cooking.daily">
</head><body><meta property="og:title" content="late"></body></html>`))
	if m.Title != "OG & title" || m.Image != "/img/o.jpg" || m.Author != "@cooking.daily" {
		t.Fatalf("got %+v", m)
	}
}

func TestParseHeadFallbacks(t *testing.T) {
	m := parseHead(strings.NewReader("<head><title> Just a\x01 title </title>" + `<meta name="twitter:image" content="//cdn/x.png"><meta property="article:author" content="Ann"></head>`))
	if m.Title != "Just a title" || m.Image != "//cdn/x.png" || m.Author != "Ann" {
		t.Fatalf("got %+v", m)
	}
}

func TestParseHeadStopsAtBody(t *testing.T) {
	m := parseHead(strings.NewReader(`<html><body><meta property="og:title" content="in body"><title>x</title>`))
	if m.Title != "" {
		t.Fatalf("metadata after <body> must be ignored, got %+v", m)
	}
}

func TestParseHeadSanitizes(t *testing.T) {
	long := strings.Repeat("я", 400)
	m := parseHead(strings.NewReader(`<head><meta property="og:title" content="<script>alert(1)</script>` + long + `"><meta name="author" content="` + long + `"></head>`))
	if !strings.HasPrefix(m.Title, "<script>alert(1)</script>") {
		t.Fatalf("title must stay plain text, got %.40q", m.Title)
	}
	if n := utf8.RuneCountInString(m.Title); n != maxTitleRunes {
		t.Fatalf("title has %d runes, want %d", n, maxTitleRunes)
	}
	if n := utf8.RuneCountInString(m.Author); n != maxAuthorRunes {
		t.Fatalf("author has %d runes, want %d", n, maxAuthorRunes)
	}
}

func TestCleanText(t *testing.T) {
	if got := cleanText("a\x00b​\n\t c‮", 10); got != "ab c" {
		t.Fatalf("cleanText = %q", got)
	}
}
