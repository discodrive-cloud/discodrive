package links

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeRejects(t *testing.T) {
	for _, raw := range []string{
		"", "javascript:alert(1)", "data:text/html,x", "file:///etc/passwd", "ftp://x.org/a",
		"https://u:p@example.org/", "https://", "/relative/path", "https://x.org/" + strings.Repeat("a", 2049),
	} {
		if _, err := Normalize(raw); !errors.Is(err, ErrInvalidURL) {
			t.Errorf("Normalize(%.40q) err = %v, want ErrInvalidURL", raw, err)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"https://www.instagram.com/reel/ABC/?igsh=xyz&utm_source=ig_web": "https://www.instagram.com/reel/ABC/",
		"HTTPS://WWW.Reddit.com/r/x#frag":                                "https://www.reddit.com/r/x",
		"https://youtu.be/id?si=q":                                       "https://youtu.be/id",
		"https://m.youtube.com/watch?v=1&feature=share":                  "https://www.youtube.com/watch?v=1",
		"https://x.com/a/status/1?s=20&t=abc":                            "https://x.com/a/status/1",
		"https://example.org/search?q=1&s=2":                             "https://example.org/search?q=1&s=2",
		"https://www.threads.com/@a/post/1?xmt=AQ&igshid=1":              "https://www.threads.com/@a/post/1?xmt=AQ",
		"https://mobile.twitter.com/a?fbclid=1&gclid=2":                  "https://twitter.com/a",
		"  https://example.org/a  ":                                      "https://example.org/a",
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestSourceOf(t *testing.T) {
	cases := map[string]string{
		"www.instagram.com": "instagram", "instagram.com": "instagram",
		"www.threads.net": "threads", "threads.com": "threads",
		"www.reddit.com": "reddit", "old.reddit.com": "reddit", "redd.it": "reddit",
		"youtu.be": "youtube", "www.youtube.com": "youtube",
		"twitter.com": "x", "x.com": "x",
		"vm.tiktok.com": "tiktok", "example.org": "other", "notinstagram.com": "other",
	}
	for host, want := range cases {
		if got := SourceOf(host); got != want {
			t.Errorf("SourceOf(%q) = %q, want %q", host, got, want)
		}
	}
}
