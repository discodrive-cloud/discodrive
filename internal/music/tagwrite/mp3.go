package tagwrite

import (
	"strconv"
	"strings"

	"github.com/bogem/id3v2/v2"
)

type mp3Writer struct{}

func (mp3Writer) Read(path string) (Tags, bool, error) {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return Tags{}, false, err
	}
	defer tag.Close()

	var t Tags
	if v := tag.Title(); v != "" {
		t.Title = &v
	}
	if v := tag.Artist(); v != "" {
		t.Artist = &v
	}
	if v := tag.Album(); v != "" {
		t.Album = &v
	}
	if v := tag.GetTextFrame(tag.CommonID("Band/Orchestra/Accompaniment")).Text; v != "" {
		t.AlbumArtist = &v
	}
	if v := tag.Genre(); v != "" {
		t.Genre = &v
	}
	// A v2.4 recording time may carry more than the year ("2019-12-05T18:30").
	if n := leadingInt(tag.Year()); n > 0 {
		t.Year = &n
	}
	if v := tag.GetTextFrame(tag.CommonID("Track number/Position in set")).Text; v != "" {
		if n := leadingInt(v); n > 0 {
			t.Track = &n
		}
	}
	if v := tag.GetTextFrame(tag.CommonID("Part of a set")).Text; v != "" {
		if n := leadingInt(v); n > 0 {
			t.Disc = &n
		}
	}

	hasCover := len(tag.GetFrames(tag.CommonID("Attached picture"))) > 0
	return t, hasCover, nil
}

func (mp3Writer) Apply(path string, t Tags, cc CoverChange, cover *Cover) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return err
	}
	defer tag.Close()

	// Normalize to clean ID3v2.4 / UTF-8 before applying changes. Library files
	// in the wild carry malformed UTF-16 frames (odd byte counts — seen in both
	// text frames and the APIC description) that bogem tolerates but writes back
	// as raw bytes; stricter readers (dhowden, players like VLC) then reject the
	// tag, so the file indexes as "Unknown" or a field (album/cover) disappears.
	// Re-encoding every frame from its decoded value yields a portable tag, and
	// UTF-8 also represents Cyrillic and other non-Latin text correctly.
	normalizeMP3(tag)

	setText := func(id string, p *string) {
		if p == nil {
			return
		}
		if *p == "" {
			tag.DeleteFrames(id)
			return
		}
		tag.AddTextFrame(id, tag.DefaultEncoding(), *p)
	}
	if t.Title != nil {
		if *t.Title == "" {
			tag.DeleteFrames(tag.CommonID("Title/Songname/Content description"))
		} else {
			tag.SetTitle(*t.Title)
		}
	}
	if t.Artist != nil {
		if *t.Artist == "" {
			tag.DeleteFrames(tag.CommonID("Lead artist/Lead performer/Soloist/Performing group"))
		} else {
			tag.SetArtist(*t.Artist)
		}
	}
	if t.Album != nil {
		if *t.Album == "" {
			tag.DeleteFrames(tag.CommonID("Album/Movie/Show title"))
		} else {
			tag.SetAlbum(*t.Album)
		}
	}
	setText(tag.CommonID("Band/Orchestra/Accompaniment"), t.AlbumArtist)
	if t.Genre != nil {
		if *t.Genre == "" {
			tag.DeleteFrames(tag.CommonID("Content type"))
		} else {
			tag.SetGenre(*t.Genre)
		}
	}
	if t.Year != nil {
		setIntText(tag, tag.CommonID("Year"), *t.Year)
	}
	if t.Track != nil {
		setIntText(tag, tag.CommonID("Track number/Position in set"), *t.Track)
	}
	if t.Disc != nil {
		setIntText(tag, tag.CommonID("Part of a set"), *t.Disc)
	}

	switch cc {
	case CoverRemove:
		tag.DeleteFrames(tag.CommonID("Attached picture"))
	case CoverReplace:
		tag.DeleteFrames(tag.CommonID("Attached picture"))
		if cover != nil {
			tag.AddAttachedPicture(id3v2.PictureFrame{
				Encoding:    tag.DefaultEncoding(),
				MimeType:    cover.Mime,
				PictureType: id3v2.PTFrontCover,
				Description: "Front cover",
				Picture:     cover.Data,
			})
		}
	}

	return tag.Save()
}

func (mp3Writer) Cover(path string) ([]byte, string, bool) {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return nil, "", false
	}
	defer tag.Close()
	frames := tag.GetFrames(tag.CommonID("Attached picture"))
	for _, f := range frames {
		if pic, ok := f.(id3v2.PictureFrame); ok && len(pic.Picture) > 0 {
			mime := pic.MimeType
			if mime == "" {
				mime = "image/jpeg"
			}
			return pic.Picture, mime, true
		}
	}
	return nil, "", false
}

// normalizeMP3 rewrites the tag as clean ID3v2.4 / UTF-8: every text frame is
// re-encoded from its decoded value (dropping any malformed raw bytes carried
// over from the source file) and the attached picture is re-added with a clean,
// empty description. Deprecated v2.3 date frames are folded into v2.4's TDRC so
// the year survives the version upgrade.
func normalizeMP3(tag *id3v2.Tag) {
	tag.SetVersion(4)
	tag.SetDefaultEncoding(id3v2.EncodingUTF8)

	// Collect first — mutating the frame map mid-iteration is unsafe.
	type textFrame struct{ id, text string }
	var texts []textFrame
	for id, frames := range tag.AllFrames() {
		for _, fr := range frames {
			if tf, ok := fr.(id3v2.TextFrame); ok {
				texts = append(texts, textFrame{id, tf.Text})
			}
		}
	}
	picID := tag.CommonID("Attached picture")
	var pics []id3v2.PictureFrame
	for _, fr := range tag.GetFrames(picID) {
		if pf, ok := fr.(id3v2.PictureFrame); ok {
			pics = append(pics, pf)
		}
	}

	// v2.3 splits the recording date over TYER (year), TDAT (DDMM) and TIME (HHMM);
	// v2.4 has the one TDRC. A tag keeps a single text frame per id, so the parts are
	// assembled into one value: writing each of them as TDRC left whichever came last
	// out of the map, and "0512" for a year.
	old := map[string]string{}
	for _, tx := range texts {
		switch tx.id {
		case "TYER", "TDAT", "TIME", "TRDA", "TDRC":
			old[tx.id] = strings.TrimSpace(tx.text)
			tag.DeleteFrames(tx.id)
			continue
		}
		tag.DeleteFrames(tx.id)
		tag.AddTextFrame(tx.id, id3v2.EncodingUTF8, tx.text)
	}
	if when := recordingTime(old); when != "" {
		tag.AddTextFrame("TDRC", id3v2.EncodingUTF8, when)
	}
	tag.DeleteFrames(picID)
	for _, pf := range pics {
		tag.AddAttachedPicture(id3v2.PictureFrame{
			Encoding:    id3v2.EncodingUTF8,
			MimeType:    pf.MimeType,
			PictureType: pf.PictureType,
			Description: "",
			Picture:     pf.Picture,
		})
	}
}

// recordingTime is the v2.4 TDRC value for a tag's date frames: an existing TDRC as it
// is, else the v2.3 year with the day and time when they are well-formed. TRDA is free
// text and has no place in a timestamp.
func recordingTime(f map[string]string) string {
	if f["TDRC"] != "" {
		return f["TDRC"]
	}
	year := f["TYER"]
	if len(year) != 4 || !allDigits(year) {
		return year // nothing, or something unusual: keep what was there
	}
	date := f["TDAT"] // DDMM
	if len(date) != 4 || !allDigits(date) {
		return year
	}
	out := year + "-" + date[2:] + "-" + date[:2]
	if clock := f["TIME"]; len(clock) == 4 && allDigits(clock) { // HHMM
		out += "T" + clock[:2] + ":" + clock[2:]
	}
	return out
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func setIntText(tag *id3v2.Tag, id string, n int) {
	if n <= 0 {
		tag.DeleteFrames(id)
		return
	}
	tag.AddTextFrame(id, tag.DefaultEncoding(), strconv.Itoa(n))
}

// leadingInt parses the leading integer of values like "3" or "3/12".
func leadingInt(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
