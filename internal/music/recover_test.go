package music

import (
	"strings"
	"testing"
)

// Parsers of user files come from third-party libraries; a panic on one malformed file
// must fail that file, not take the server (and its background scan) down.
func TestRecoverParseTurnsPanicIntoError(t *testing.T) {
	parse := func() (err error) {
		defer recoverParse(&err)
		panic("index out of range")
	}
	err := parse()
	if err == nil || !strings.Contains(err.Error(), "index out of range") {
		t.Fatalf("got %v, want an error carrying the panic", err)
	}
}
