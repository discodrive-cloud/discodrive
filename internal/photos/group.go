package photos

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// liveFallbackWindow is how far apart a still and a video named alike may have been
// taken to count as one Live Photo when neither carries a content identifier.
const liveFallbackWindow = 3 * time.Second

const (
	roleLiveVideo = "live-video"
	roleOriginal  = "original"
)

type link struct {
	parent pgtype.UUID
	role   string
}

// Regroup recomputes every parent/role link of the user's library and writes the ones
// that changed:
//   - an edited photo (IMG_E1234.* next to IMG_1234.*, or <stem>_E.* next to <stem>.*)
//     becomes the parent of its original (role "original");
//   - a video with the same Live Photo content identifier as a photo, or else in the same
//     folder with the same stem and taken within 3 s, becomes the photo's child (role
//     "live-video"); when that photo is an original, the child goes to its edit, so the
//     card that shows keeps its Live video.
//
// Trashed and live assets pair only among themselves: the trash view and the feed each
// show whole groups.
func (ix *Indexer) Regroup(ctx context.Context, userID pgtype.UUID) error {
	l := ix.userLock(userID)
	l.Lock()
	defer l.Unlock()
	return ix.regroup(ctx, userID)
}

// regroup is Regroup with the user's lock held.
func (ix *Indexer) regroup(ctx context.Context, userID pgtype.UUID) error {
	rows, err := ix.q.ListPhotoGroupCandidates(ctx, userID)
	if err != nil {
		return err
	}
	want := planGroups(rows)
	for _, r := range rows {
		w := want[r.ID]
		if r.ParentID == w.parent && r.Role.String == w.role && r.Role.Valid == (w.role != "") {
			continue
		}
		if err := ix.q.SetPhotoParent(ctx, db.SetPhotoParentParams{
			ID: r.ID, ParentID: w.parent, Role: text(w.role)}); err != nil {
			return err
		}
	}
	return nil
}

var (
	appleEditRe  = regexp.MustCompile(`(?i)^img_e(\d+)$`) // IMG_E1234 → IMG_1234
	suffixEditRe = regexp.MustCompile(`(?i)^(.+)_e$`)     // name_E → name
)

// editedStem returns the stem of the original an edited file's stem points to.
func editedStem(stem string) (string, bool) {
	if g := appleEditRe.FindStringSubmatch(stem); g != nil {
		return "img_" + g[1], true
	}
	if g := suffixEditRe.FindStringSubmatch(stem); g != nil {
		return g[1], true
	}
	return "", false
}

func stemOf(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
}

// planGroups returns the wanted link of every asset that has one.
func planGroups(rows []db.ListPhotoGroupCandidatesRow) map[pgtype.UUID]link {
	want := map[pgtype.UUID]link{}
	var live, trashed []*db.ListPhotoGroupCandidatesRow
	for i := range rows {
		if rows[i].TrashedAt.Valid {
			trashed = append(trashed, &rows[i])
		} else {
			live = append(live, &rows[i])
		}
	}
	planPartition(live, want)
	planPartition(trashed, want)
	return want
}

type folderStem struct {
	folder pgtype.UUID
	stem   string
}

func planPartition(rows []*db.ListPhotoGroupCandidatesRow, want map[pgtype.UUID]link) {
	sort.Slice(rows, func(i, j int) bool { return lessUUID(rows[i].ID, rows[j].ID) })
	photosByStem := map[folderStem][]*db.ListPhotoGroupCandidatesRow{}
	for _, r := range rows {
		if r.Kind == "photo" {
			k := folderStem{r.FolderID, stemOf(r.Name)}
			photosByStem[k] = append(photosByStem[k], r)
		}
	}

	// Edited pairs. The first edit (by id) of a stem wins; an edit is never itself an
	// original.
	isEdit := map[pgtype.UUID]bool{}
	for _, r := range rows {
		if r.Kind != "photo" {
			continue
		}
		if _, ok := editedStem(stemOf(r.Name)); ok {
			isEdit[r.ID] = true
		}
	}
	top := map[pgtype.UUID]pgtype.UUID{} // original → its edit
	for _, r := range rows {
		if !isEdit[r.ID] {
			continue
		}
		orig, _ := editedStem(stemOf(r.Name))
		for _, o := range photosByStem[folderStem{r.FolderID, orig}] {
			if o.ID == r.ID || isEdit[o.ID] {
				continue
			}
			if _, taken := top[o.ID]; taken {
				continue
			}
			top[o.ID] = r.ID
			want[o.ID] = link{parent: r.ID, role: roleOriginal}
		}
	}
	shown := func(photo pgtype.UUID) pgtype.UUID {
		if e, ok := top[photo]; ok {
			return e
		}
		return photo
	}

	// Live Photos by content identifier: the still that shows (not an original when
	// another still of the identifier is not one) takes every video of it.
	type cidGroup struct {
		photos, videos []*db.ListPhotoGroupCandidatesRow
	}
	cids := map[string]*cidGroup{}
	var cidOrder []string
	for _, r := range rows {
		if !r.LiveCid.Valid || r.LiveCid.String == "" || (r.Kind != "photo" && r.Kind != "video") {
			continue
		}
		g := cids[r.LiveCid.String]
		if g == nil {
			g = &cidGroup{}
			cids[r.LiveCid.String] = g
			cidOrder = append(cidOrder, r.LiveCid.String)
		}
		if r.Kind == "photo" {
			g.photos = append(g.photos, r)
		} else {
			g.videos = append(g.videos, r)
		}
	}
	paired := map[pgtype.UUID]bool{}
	for _, c := range cidOrder {
		g := cids[c]
		if len(g.photos) == 0 || len(g.videos) == 0 {
			continue
		}
		still := g.photos[0]
		for _, p := range g.photos {
			if _, orig := top[p.ID]; !orig {
				still = p
				break
			}
		}
		for _, v := range g.videos {
			want[v.ID] = link{parent: shown(still.ID), role: roleLiveVideo}
			paired[v.ID] = true
		}
	}

	// Fallback: same folder, same stem, taken within the window; the closest still wins.
	// Files whose identifiers both exist and differ are not one Live Photo.
	for _, v := range rows {
		if v.Kind != "video" || paired[v.ID] || !v.TakenAt.Valid {
			continue
		}
		var best *db.ListPhotoGroupCandidatesRow
		var bestDelta time.Duration
		for _, p := range photosByStem[folderStem{v.FolderID, stemOf(v.Name)}] {
			if !p.TakenAt.Valid {
				continue
			}
			if v.LiveCid.String != "" && p.LiveCid.String != "" && v.LiveCid.String != p.LiveCid.String {
				continue
			}
			d := p.TakenAt.Time.Sub(v.TakenAt.Time)
			if d < 0 {
				d = -d
			}
			if d <= liveFallbackWindow && (best == nil || d < bestDelta) {
				best, bestDelta = p, d
			}
		}
		if best != nil {
			want[v.ID] = link{parent: shown(best.ID), role: roleLiveVideo}
		}
	}
}

func lessUUID(a, b pgtype.UUID) bool {
	for i := range a.Bytes {
		if a.Bytes[i] != b.Bytes[i] {
			return a.Bytes[i] < b.Bytes[i]
		}
	}
	return false
}
