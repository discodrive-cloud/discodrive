package tagwrite

import (
	"testing"

	"github.com/bogem/id3v2/v2"
)

// An ID3v2.3 tag keeps the year in TYER and day+month in TDAT. Both were folded into the
// one v2.4 TDRC frame, whichever came last out of a map winning: editing any field could
// turn the year 2019 into "0512".
func TestMP3_EditKeepsTheYearOfAV23Tag(t *testing.T) {
	for i := 0; i < 40; i++ { // the old outcome depended on map order
		path := copyToTemp(t, "sample.mp3")
		tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
		if err != nil {
			t.Fatal(err)
		}
		tag.DeleteAllFrames()
		tag.SetVersion(3)
		tag.AddTextFrame("TIT2", id3v2.EncodingISO, "Song")
		tag.AddTextFrame("TYER", id3v2.EncodingISO, "2019")
		tag.AddTextFrame("TDAT", id3v2.EncodingISO, "0512")
		tag.AddTextFrame("TIME", id3v2.EncodingISO, "1830")
		if err := tag.Save(); err != nil {
			t.Fatal(err)
		}
		tag.Close()

		w, _ := For("mp3")
		if err := w.Apply(path, Tags{Genre: strp("Jazz")}, CoverKeep, nil); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		got, _, err := w.Read(path)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got.Year == nil || *got.Year != 2019 {
			y := 0
			if got.Year != nil {
				y = *got.Year
			}
			t.Fatalf("run %d: year after editing the genre = %d, want 2019", i, y)
		}
	}
}
