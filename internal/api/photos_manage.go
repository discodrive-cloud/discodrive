package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/photos"
	"discodrive/internal/storage"
)

// Favorites, the hidden part, deletion and the gallery trash.

const maxPhotoBatch = 1000

// maxPhotoEditBatch bounds a metadata edit: it runs synchronously, a file at a time.
const maxPhotoEditBatch = 200

// photoIDs parses the ids of a batch request.
func photoIDs(raw []string) ([]pgtype.UUID, bool) {
	if len(raw) == 0 || len(raw) > maxPhotoBatch {
		return nil, false
	}
	out := make([]pgtype.UUID, 0, len(raw))
	for _, r := range raw {
		id, err := db.ParseUUID(r)
		if err != nil {
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

// ownCards loads the caller's assets named by a batch request. Any foreign or unknown
// id, and any hidden card without the unlock token, fails the whole request with 404.
// trashed selects the trash (restore) instead of the live gallery.
func (s *Server) ownCards(w http.ResponseWriter, r *http.Request, uid pgtype.UUID, raw []string, trashed bool) ([]pgtype.UUID, bool) {
	ids, ok := photoIDs(raw)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid ids")
		return nil, false
	}
	rows, err := s.q.ListOwnPhotoAssets(r.Context(), db.ListOwnPhotoAssetsParams{UserID: uid, Ids: ids})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	byID := map[pgtype.UUID]db.PhotoAsset{}
	for _, a := range rows {
		byID[a.ID] = a
	}
	unlocked := false
	checked := false
	for _, id := range ids {
		a, found := byID[id]
		if found && a.TrashedAt.Valid != trashed {
			found = false
		}
		if found && a.Hidden {
			if !checked {
				unlocked, checked = s.photosUnlocked(r), true
			}
			found = unlocked
		}
		if !found {
			writeError(w, http.StatusNotFound, "not found")
			return nil, false
		}
	}
	return ids, true
}

// PATCH /me/photos/v1/assets {"ids":[], "favorite":bool?, "hidden":bool?} — acts on whole
// cards: the live video and the original follow their parent.
func (s *Server) handlePatchPhotoAssets(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs      []string `json:"ids"`
		Favorite *bool    `json:"favorite"`
		Hidden   *bool    `json:"hidden"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Favorite == nil && req.Hidden == nil) {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	ids, ok := s.ownCards(w, r, uid, req.IDs, false)
	if !ok {
		return
	}
	p := db.SetPhotoCardFlagsParams{UserID: uid, Ids: ids}
	if req.Favorite != nil {
		p.Favorite = pgtype.Bool{Bool: *req.Favorite, Valid: true}
	}
	if req.Hidden != nil {
		p.Hidden = pgtype.Bool{Bool: *req.Hidden, Valid: true}
	}
	if err := s.q.SetPhotoCardFlags(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": len(ids)})
}

// PUT /me/photos/v1/assets/metadata {"ids":[], "takenAt":RFC3339?, "tzOffsetMin":int?,
// "shiftMinutes":int?, "place":{"lat","lon"}|null?} — sets the capture moment and/or the
// place of whole cards (a shift moves each card from its own date). The values go into the
// files (EXIF, or an XMP sidecar next to video and RAW) as a new file version. Always 200
// with the outcome per card: updated, conflicts (a file changed meanwhile; left alone),
// failed ({id,error}) and partial (a multi-file card written in part: do not retry a
// shift on it). At most 200 ids per request. A takenAt in UTC ("Z" or +00:00) without
// tzOffsetMin is a wall-clock time with no known zone; any other zone also sets the offset.
func (s *Server) handlePutPhotoMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) || s.photoEditor == nil {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs          []string        `json:"ids"`
		TakenAt      *time.Time      `json:"takenAt"`
		TZOffsetMin  *int            `json:"tzOffsetMin"`
		ShiftMinutes *int            `json:"shiftMinutes"`
		Place        json.RawMessage `json:"place"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	ed := photos.Edit{SetTime: req.TakenAt, TZOffsetMin: req.TZOffsetMin}
	if req.ShiftMinutes != nil {
		// Bounded to a century: the sum must stay a date a camera could have written.
		if *req.ShiftMinutes > 100*366*24*60 || *req.ShiftMinutes < -100*366*24*60 {
			writeError(w, http.StatusBadRequest, "invalid shift")
			return
		}
		ed.ShiftMin = *req.ShiftMinutes
	}
	if req.TZOffsetMin != nil && (*req.TZOffsetMin < -14*60 || *req.TZOffsetMin > 14*60) {
		writeError(w, http.StatusBadRequest, "invalid offset")
		return
	}
	if req.Place != nil {
		ed.SetPlace = true
		if string(bytes.TrimSpace(req.Place)) != "null" {
			var pl struct {
				Lat *float64 `json:"lat"`
				Lon *float64 `json:"lon"`
			}
			if err := json.Unmarshal(req.Place, &pl); err != nil || pl.Lat == nil || pl.Lon == nil {
				writeError(w, http.StatusBadRequest, "invalid place")
				return
			}
			ed.Lat, ed.Lon = pl.Lat, pl.Lon
		}
	}
	if req.ShiftMinutes != nil && req.TakenAt != nil {
		writeError(w, http.StatusBadRequest, "takenAt and shiftMinutes exclude each other")
		return
	}
	if req.TakenAt != nil && req.TZOffsetMin == nil {
		// The time's own zone is the place's zone when the client did not say otherwise.
		if _, off := req.TakenAt.Zone(); req.TakenAt.Location() != time.UTC {
			o := off / 60
			ed.TZOffsetMin = &o
		}
	}
	if req.TakenAt == nil && req.ShiftMinutes == nil && !ed.SetPlace {
		writeError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if len(req.IDs) > maxPhotoEditBatch {
		writeError(w, http.StatusBadRequest, "too many ids")
		return
	}
	if _, ok := s.ownCards(w, r, uid, req.IDs, false); !ok {
		return
	}
	res, err := s.photoEditor.Apply(r.Context(), auth.UserID(r.Context()), req.IDs, ed)
	if errors.Is(err, photos.ErrBadEdit) {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err != nil {
		log.Printf("discodrive: photos edit: %v", err)
		writeStorageErr(w, err)
		return
	}
	type failure struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	failed := make([]failure, len(res.Failed))
	for i, f := range res.Failed {
		failed[i] = failure{f.ID, f.Error}
	}
	nz := func(l []string) []string { return append([]string{}, l...) }
	writeJSON(w, http.StatusOK, map[string]any{"updated": nz(res.Updated), "conflicts": nz(res.Conflicts),
		"failed": failed, "partial": nz(res.Partial), "versionBytes": res.VersionBytes})
}

// POST /me/photos/v1/assets/delete {"ids":[]} — sends the cards' files (children and
// every copy of the same content in the sources included) to the file trash; the cards
// move to the gallery trash.
func (s *Server) handleDeletePhotoAssets(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	ids, ok := s.ownCards(w, r, uid, req.IDs, false)
	if !ok {
		return
	}
	nodes, err := s.q.ListPhotoDeleteNodes(r.Context(), db.ListPhotoDeleteNodesParams{UserID: uid, Ids: ids})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	deleted := 0
	for _, n := range nodes {
		nid := db.UUIDString(n)
		err := s.files.Delete(r.Context(), auth.UserID(r.Context()), nid)
		if errors.Is(err, storage.ErrNotFound) {
			continue // already gone (a folder trashed meanwhile)
		}
		if err != nil {
			log.Printf("discodrive: photos delete %s: %v", nid, err)
			writeStorageErr(w, err)
			return
		}
		deleted++
		// The change-log pass would do the same; doing it now keeps the answer current.
		if err := s.photos.Remove(r.Context(), nid); err != nil {
			log.Printf("discodrive: photos trash %s: %v", nid, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"deletedNodes": deleted})
}

// POST /me/photos/v1/hidden/unlock {"approvalToken"} -> {"token","expiresAt"}
func (s *Server) handlePhotosHiddenUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ApprovalToken string `json:"approvalToken"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	tok, exp, err := s.auth.IssuePhotosHidden(r.Context(), auth.UserID(r.Context()), req.ApprovalToken)
	if errors.Is(err, auth.ErrApproval) {
		writeError(w, http.StatusUnauthorized, "identity confirmation failed")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": tok, "expiresAt": exp.UTC().Format(time.RFC3339)})
}

// GET /me/photos/v1/hidden/buckets — needs X-Photos-Hidden; the shape of /buckets.
func (s *Server) handlePhotosHiddenBuckets(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	if !s.photosUnlocked(r) {
		writeError(w, http.StatusForbidden, "hidden_locked")
		return
	}
	rows, err := s.q.ListHiddenPhotoMonthBuckets(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, b := range rows {
		out = append(out, map[string]any{"month": b.Month.Time.Format("2006-01"), "count": b.Count})
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /me/photos/v1/hidden?month=YYYY-MM — needs X-Photos-Hidden.
func (s *Server) handlePhotosHidden(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	if !s.photosUnlocked(r) {
		writeError(w, http.StatusForbidden, "hidden_locked")
		return
	}
	month, err := time.Parse("2006-01", r.URL.Query().Get("month"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid month")
		return
	}
	assets, err := s.q.ListHiddenPhotoAssets(r.Context(), db.ListHiddenPhotoAssetsParams{
		UserID: uid, Month: pgtype.Timestamp{Time: month, Valid: true}})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	rows, err := s.photoRows(r, assets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// GET /me/photos/v1/trash — the trashed cards, newest deletion first. Hidden ones show
// only with the unlock token.
func (s *Server) handlePhotosTrash(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	assets, err := s.q.ListTrashedPhotoAssets(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !s.photosUnlocked(r) {
		kept := assets[:0]
		for _, a := range assets {
			if !a.Hidden {
				kept = append(kept, a)
			}
		}
		assets = kept
	}
	rows, err := s.photoRows(r, assets)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	type trashRow struct {
		photoRowDTO
		TrashedAt string `json:"trashedAt"`
	}
	out := make([]trashRow, len(rows))
	for i, row := range rows {
		out[i] = trashRow{row, assets[i].TrashedAt.Time.UTC().Format(time.RFC3339)}
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /me/photos/v1/trash/restore {"ids":[]} — brings the cards back through the file
// trash's own restore (a vanished folder sends the file to the root, a taken name gets
// " (restored)"); the index then un-trashes the cards.
func (s *Server) handlePhotosRestore(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	ids, ok := s.ownCards(w, r, uid, req.IDs, true)
	if !ok {
		return
	}
	nodes, err := s.q.ListPhotoRestoreNodes(r.Context(), db.ListPhotoRestoreNodesParams{UserID: uid, Ids: ids})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	restored := 0
	for _, n := range nodes {
		nid := db.UUIDString(n)
		node, err := s.files.Undelete(r.Context(), auth.UserID(r.Context()), nid)
		if errors.Is(err, storage.ErrNotFound) {
			continue // restored meanwhile with its folder, or purged
		}
		if err != nil {
			log.Printf("discodrive: photos restore %s: %v", nid, err)
			writeStorageErr(w, err)
			return
		}
		restored++
		if err := s.photos.Index(r.Context(), auth.UserID(r.Context()), nid, node.DiskPath.String); err != nil {
			log.Printf("discodrive: photos untrash %s: %v", nid, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"restoredNodes": restored})
}
