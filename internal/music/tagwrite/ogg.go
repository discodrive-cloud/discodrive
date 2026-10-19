package tagwrite

import (
	"os"
)

// oggWriter is a read-only implementation for OGG files (Vorbis or Opus).
// Pure-Go Ogg Vorbis comment writing requires re-paginating the bitstream;
// no vetted cgo-free library exists for this. Apply always returns ErrReadOnly.
type oggWriter struct{}

func (oggWriter) Read(path string) (Tags, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return Tags{}, false, err
	}
	defer f.Close()

	m, err := ReadFrom(f)
	if err != nil {
		return Tags{}, false, err
	}

	t := tagsFromDhowden(m)
	hasCover := m.Picture() != nil
	return t, hasCover, nil
}

func (oggWriter) Apply(_ string, _ Tags, _ CoverChange, _ *Cover) error {
	return ErrReadOnly
}

func (oggWriter) Cover(path string) ([]byte, string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()

	m, err := ReadFrom(f)
	if err != nil {
		return nil, "", false
	}

	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, "", false
	}
	mime := pic.MIMEType
	if mime == "" {
		mime = "image/jpeg"
	}
	return pic.Data, mime, true
}
