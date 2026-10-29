package photos

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

type cand struct {
	name, kind, cid string
	folder          byte
	at              time.Duration // after a fixed base time
	trashed         bool
}

// rows builds group candidates; asset ids are 1, 2, … in the given order.
func rows(cs ...cand) []db.ListPhotoGroupCandidatesRow {
	base := time.Date(2023, 8, 1, 10, 0, 0, 0, time.UTC)
	var out []db.ListPhotoGroupCandidatesRow
	for i, c := range cs {
		r := db.ListPhotoGroupCandidatesRow{
			ID:       uid(byte(i + 1)),
			Kind:     c.kind,
			Name:     c.name,
			FolderID: uid(100 + c.folder),
			TakenAt:  pgtype.Timestamptz{Time: base.Add(c.at), Valid: true},
			LiveCid:  text(c.cid),
		}
		if c.trashed {
			r.TrashedAt = pgtype.Timestamptz{Time: base, Valid: true}
		}
		out = append(out, r)
	}
	return out
}

func uid(b byte) pgtype.UUID {
	var u pgtype.UUID
	u.Bytes[15], u.Valid = b, true
	return u
}

func wantLink(t *testing.T, plan map[pgtype.UUID]link, child, parent byte, role string) {
	t.Helper()
	got := plan[uid(child)]
	if got.parent != uid(parent) || got.role != role {
		t.Fatalf("asset %d: link %+v, want parent %d role %s", child, got, parent, role)
	}
}

func wantNone(t *testing.T, plan map[pgtype.UUID]link, ids ...byte) {
	t.Helper()
	for _, i := range ids {
		if l, ok := plan[uid(i)]; ok {
			t.Fatalf("asset %d: link %+v, want none", i, l)
		}
	}
}

// Videos join the still with their content identifier regardless of folder, name and
// time; a still that is an original hands its video to its edit; a trashed video does
// not join a live still.
func TestGroup_LiveByCIDPlan(t *testing.T) {
	plan := planGroups(rows(
		cand{name: "IMG_0001.HEIC", kind: "photo", cid: "A"},                   // 1
		cand{name: "x.mov", kind: "video", cid: "A", folder: 2, at: time.Hour}, // 2
		cand{name: "IMG_0002.HEIC", kind: "photo", cid: "B"},                   // 3: original of 4
		cand{name: "IMG_E0002.HEIC", kind: "photo", cid: "B"},                  // 4
		cand{name: "IMG_0002.MOV", kind: "video", cid: "B"},                    // 5
		cand{name: "IMG_0003.HEIC", kind: "photo", cid: "C"},                   // 6
		cand{name: "IMG_0003.MOV", kind: "video", cid: "C", trashed: true},     // 7
		cand{name: "IMG_0009.MOV", kind: "video", cid: "Z"},                    // 8: no still
	))
	wantLink(t, plan, 2, 1, roleLiveVideo)
	wantLink(t, plan, 3, 4, roleOriginal)
	wantLink(t, plan, 5, 4, roleLiveVideo)
	wantNone(t, plan, 1, 4, 6, 7, 8)
}

// Without identifiers: same folder, same stem (any case), within 3 s; the closest still
// wins; differing identifiers never pair.
func TestGroup_LiveFallbackByName(t *testing.T) {
	plan := planGroups(rows(
		cand{name: "IMG_0010.JPG", kind: "photo"},                      // 1
		cand{name: "img_0010.mov", kind: "video", at: 2 * time.Second}, // 2 → 1
		cand{name: "IMG_0011.JPG", kind: "photo"},                      // 3
		cand{name: "IMG_0011.MOV", kind: "video", at: 4 * time.Second}, // 4: too far
		cand{name: "IMG_0012.JPG", kind: "photo", folder: 1},           // 5
		cand{name: "IMG_0012.MOV", kind: "video"},                      // 6: other folder
		cand{name: "IMG_0013.JPG", kind: "photo", cid: "P"},            // 7
		cand{name: "IMG_0013.MOV", kind: "video", cid: "Q"},            // 8: ids differ
		cand{name: "IMG_0014.JPG", kind: "photo", cid: "P2"},           // 9
		cand{name: "IMG_0014.MOV", kind: "video", at: -time.Second},    // 10 → 9 (one id only)
	))
	wantLink(t, plan, 2, 1, roleLiveVideo)
	wantLink(t, plan, 10, 9, roleLiveVideo)
	wantNone(t, plan, 1, 3, 4, 5, 6, 7, 8, 9)
}

// IMG_E1234 and <stem>_E edits become the parents of their originals in the same folder.
func TestGroup_EditedPair(t *testing.T) {
	plan := planGroups(rows(
		cand{name: "IMG_1234.HEIC", kind: "photo"},           // 1 → 2
		cand{name: "IMG_E1234.JPG", kind: "photo"},           // 2
		cand{name: "holiday.jpg", kind: "photo"},             // 3 → 4
		cand{name: "holiday_E.jpg", kind: "photo"},           // 4
		cand{name: "IMG_5555.JPG", kind: "photo", folder: 1}, // 5: other folder
		cand{name: "IMG_E5555.JPG", kind: "photo"},           // 6
		cand{name: "IMG_7777.JPG", kind: "photo"},            // 7: no edit
		cand{name: "scan_01.jpg", kind: "photo"},             // 8
		cand{name: "scan_e01.jpg", kind: "photo"},            // 9: not an edit name
	))
	wantLink(t, plan, 1, 2, roleOriginal)
	wantLink(t, plan, 3, 4, roleOriginal)
	wantNone(t, plan, 2, 4, 5, 6, 7, 8, 9)
}

// A trimmed or filtered video goes up as <stem>_E.MOV beside <stem>.MOV. A Live Photo's
// videos keep to their still: IMG_E1234.MOV of an edited Live Photo is its motion.
func TestGroup_EditedVideo(t *testing.T) {
	plan := planGroups(rows(
		cand{name: "IMG_0100.MOV", kind: "video", at: 0},               // 1 → 2
		cand{name: "IMG_0100_E.MOV", kind: "video", at: time.Hour},     // 2
		cand{name: "IMG_0200.HEIC", kind: "photo", cid: "L"},           // 3 → 4
		cand{name: "IMG_E0200.HEIC", kind: "photo", cid: "L"},          // 4
		cand{name: "IMG_0200.MOV", kind: "video", cid: "L"},            // 5: Live of 4
		cand{name: "IMG_E0200.MOV", kind: "video", cid: "L"},           // 6: Live of 4
		cand{name: "IMG_0300.HEIC", kind: "photo"},                     // 7
		cand{name: "IMG_0300_E.MOV", kind: "video", at: 2 * time.Hour}, // 8: not its edit
		cand{name: "clip.mp4", kind: "video", at: 0},                   // 9
		cand{name: "clip_E.jpg", kind: "photo"},                        // 10: not its edit
	))
	wantLink(t, plan, 1, 2, roleOriginal)
	wantLink(t, plan, 3, 4, roleOriginal)
	wantLink(t, plan, 5, 4, roleLiveVideo)
	wantLink(t, plan, 6, 4, roleLiveVideo)
	wantNone(t, plan, 2, 4, 7, 8, 9, 10)
}
