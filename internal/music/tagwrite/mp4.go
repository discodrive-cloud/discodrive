package tagwrite

import (
	mp4tag "github.com/Sorrow446/go-mp4tag"
)

type mp4Writer struct{}

func (mp4Writer) Read(path string) (Tags, bool, error) {
	if err := CheckMP4File(path); err != nil {
		return Tags{}, false, err
	}
	f, err := mp4tag.Open(path)
	if err != nil {
		return Tags{}, false, err
	}
	defer f.Close()

	m, err := f.Read()
	if err != nil {
		return Tags{}, false, err
	}

	var t Tags
	if m.Title != "" {
		v := m.Title
		t.Title = &v
	}
	if m.Artist != "" {
		v := m.Artist
		t.Artist = &v
	}
	if m.Album != "" {
		v := m.Album
		t.Album = &v
	}
	if m.AlbumArtist != "" {
		v := m.AlbumArtist
		t.AlbumArtist = &v
	}
	// CustomGenre holds a free-form string; Genre is the iTunes numeric enum.
	if m.CustomGenre != "" {
		v := m.CustomGenre
		t.Genre = &v
	}
	if m.Year > 0 {
		n := int(m.Year)
		t.Year = &n
	}
	if m.TrackNumber > 0 {
		n := int(m.TrackNumber)
		t.Track = &n
	}
	if m.DiscNumber > 0 {
		n := int(m.DiscNumber)
		t.Disc = &n
	}

	hasCover := len(m.Pictures) > 0
	return t, hasCover, nil
}

func (mp4Writer) Apply(path string, t Tags, cc CoverChange, cover *Cover) error {
	if err := CheckMP4File(path); err != nil {
		return err
	}
	f, err := mp4tag.Open(path)
	if err != nil {
		return err
	}
	// f.Write closes the handle internally then reopens it. We close explicitly
	// after Write rather than deferring: a deferred close before Write runs
	// would double-close the fd in error paths where Write already closed it
	// (e.g. moveMP4 failure). See close call below for details.

	var tags mp4tag.MP4Tags
	var delStrings []string

	// For each non-nil field: non-empty/positive → SET; empty/zero → DELETE.
	if t.Title != nil {
		if *t.Title != "" {
			tags.Title = *t.Title
		} else {
			delStrings = append(delStrings, "title")
		}
	}
	if t.Artist != nil {
		if *t.Artist != "" {
			tags.Artist = *t.Artist
		} else {
			delStrings = append(delStrings, "artist")
		}
	}
	if t.Album != nil {
		if *t.Album != "" {
			tags.Album = *t.Album
		} else {
			delStrings = append(delStrings, "album")
		}
	}
	if t.AlbumArtist != nil {
		if *t.AlbumArtist != "" {
			tags.AlbumArtist = *t.AlbumArtist
		} else {
			delStrings = append(delStrings, "albumartist")
		}
	}
	if t.Genre != nil {
		if *t.Genre != "" {
			tags.CustomGenre = *t.Genre
			tags.Genre = mp4tag.GenreNone // clear numeric genre to avoid conflicts
		} else {
			delStrings = append(delStrings, "customgenre")
		}
	}
	if t.Year != nil {
		if *t.Year > 0 {
			tags.Year = int32(*t.Year)
		} else {
			delStrings = append(delStrings, "year")
		}
	}
	if t.Track != nil {
		if *t.Track > 0 {
			tags.TrackNumber = int16(*t.Track)
		} else {
			delStrings = append(delStrings, "tracknumber")
		}
	}
	if t.Disc != nil {
		if *t.Disc > 0 {
			tags.DiscNumber = int16(*t.Disc)
		} else {
			delStrings = append(delStrings, "discnumber")
		}
	}

	switch cc {
	case CoverRemove:
		delStrings = append(delStrings, "allpictures")
	case CoverReplace:
		if cover != nil {
			tags.Pictures = []*mp4tag.MP4Picture{
				{Format: mp4tag.ImageTypeAuto, Data: cover.Data},
			}
		} else {
			delStrings = append(delStrings, "allpictures")
		}
	}

	// Write closes the handle internally (then reopens it). We close here
	// unconditionally: on success this closes the newly reopened fd; on error
	// paths where the internal close already fired, f.Close() returns EBADF
	// which we intentionally ignore — the fd is already gone.
	writeErr := f.Write(&tags, delStrings)
	f.Close() //nolint:errcheck
	return writeErr
}

func (mp4Writer) Cover(path string) ([]byte, string, bool) {
	if CheckMP4File(path) != nil {
		return nil, "", false
	}
	f, err := mp4tag.Open(path)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()

	m, err := f.Read()
	if err != nil || len(m.Pictures) == 0 {
		return nil, "", false
	}

	pic := m.Pictures[0]
	mime := "image/jpeg"
	if pic.Format == mp4tag.ImageTypePNG {
		mime = "image/png"
	}
	return pic.Data, mime, true
}
