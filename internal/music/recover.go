package music

import "fmt"

// recoverParse turns a panic inside a third-party parser into an error for that one
// file, so a malformed upload cannot take down the server and its background scan.
// Use as `defer recoverParse(&err)`.
func recoverParse(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("music: parser panic: %v", r)
	}
}
