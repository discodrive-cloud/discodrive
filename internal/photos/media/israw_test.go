package media

import "testing"

func TestIsRawName(t *testing.T) {
	for name, want := range map[string]bool{
		"IMG_1170.DNG": true, "a.cr2": true, "x.NEF": true,
		"IMG_1170.jpeg": false, "a.heic": false, "noext": false,
	} {
		if got := IsRawName(name); got != want {
			t.Errorf("IsRawName(%q)=%v, want %v", name, got, want)
		}
	}
}
