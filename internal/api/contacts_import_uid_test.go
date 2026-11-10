package api

import "testing"

// A card whose UID cannot be a path segment gets a new one: stored under "foo/bar" it
// would be in the address book and out of reach of every CardDAV client.
func TestImportCardsReplacesAUIDThatCannotBeAPath(t *testing.T) {
	raw := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:foo/bar\r\nFN:Slash\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:plain-1\r\nFN:Plain\r\nEND:VCARD\r\n"
	stored := map[string]string{}
	imported, skipped, err := importCards(raw, func(uid, data string) error { stored[uid] = data; return nil })
	if err != nil || imported != 2 || skipped != 0 {
		t.Fatalf("imported %d, skipped %d, err %v", imported, skipped, err)
	}
	if _, kept := stored["plain-1"]; !kept {
		t.Error("an ordinary UID was not kept")
	}
	for uid, data := range stored {
		if !safeObjectUID(uid) {
			t.Errorf("stored under %q", uid)
		}
		if uid != "plain-1" && !containsLine(data, "UID:"+uid) {
			t.Errorf("the card stored as %q does not carry that UID:\n%s", uid, data)
		}
	}
}

func containsLine(data, line string) bool {
	for _, l := range splitLines(data) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '\n' {
			out = append(out, cur)
			cur = ""
		} else if c != '\r' {
			cur += string(c)
		}
	}
	return append(out, cur)
}
