package notify

import (
	"strings"
	"testing"
)

func TestRenderLayoutWrapsContent(t *testing.T) {
	html, err := renderLayout("<h1>Привет</h1>")
	if err != nil {
		t.Fatalf("renderLayout: %v", err)
	}
	for _, want := range []string{"DiscoDrive", "<h1>Привет</h1>", "https://discodrive.app"} {
		if !strings.Contains(html, want) {
			t.Fatalf("email is missing %q", want)
		}
	}
}
