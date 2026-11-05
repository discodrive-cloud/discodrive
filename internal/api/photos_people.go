package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/photos"
)

// WithPhotoFaces turns people on: the face queue the handlers kick and lock against.
func WithPhotoFaces(f *photos.Faces) Option {
	return func(s *Server) { s.faces = f }
}

// facesReady: people exist on this server (a face service is configured). Otherwise
// every people route is 404, as if it did not exist.
func (s *Server) facesReady(w http.ResponseWriter) bool {
	if s.faces == nil || s.photos == nil || s.thumbs == nil {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}
	return true
}

type facesSettingsDTO struct {
	Available bool  `json:"available"`
	Enabled   bool  `json:"enabled"`
	MLOnline  bool  `json:"mlOnline"`
	Done      int64 `json:"done"`
	Failed    int64 `json:"failed"`
	Pending   int64 `json:"pending"`
	Total     int64 `json:"total"`
}

func (s *Server) facesSettings(r *http.Request, uid pgtype.UUID, enabled bool) (facesSettingsDTO, error) {
	out := facesSettingsDTO{Available: s.faces != nil && s.thumbs != nil}
	if !out.Available {
		return out, nil
	}
	out.Enabled, out.MLOnline = enabled, s.faces.Online()
	p, err := s.q.FaceProgress(r.Context(), uid)
	if err != nil {
		return out, err
	}
	out.Done, out.Failed, out.Pending, out.Total = p.Done, p.Failed, p.Pending, p.Total
	return out, nil
}

// PUT /me/photos/v1/faces {"enabled":bool}
func (s *Server) handlePutPhotoFaces(w http.ResponseWriter, r *http.Request) {
	if !s.facesReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	n, err := s.q.SetFacesEnabled(r.Context(), db.SetFacesEnabledParams{UserID: uid, FacesEnabled: req.Enabled})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusConflict, "photos_off") // the gallery was never turned on
		return
	}
	if req.Enabled {
		s.faces.Kick()
	}
	out, err := s.facesSettings(r, uid, req.Enabled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type personDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Hidden    bool    `json:"hidden"`
	BirthDate *string `json:"birthDate"` // YYYY-MM-DD
	Count     int64   `json:"count"`
	Cover     *string `json:"cover"` // a face id: GET people/faces/{id}/thumb
}

func datep(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	v := d.Time.Format(time.DateOnly)
	return &v
}

// ageOn is how old someone born on birth was on the day taken (nil: no birthday, or the
// photo is older than them).
func ageOn(birth pgtype.Date, taken time.Time) *int {
	if !birth.Valid {
		return nil
	}
	b := birth.Time
	if taken.Before(time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, taken.Location())) {
		return nil
	}
	age := taken.Year() - b.Year()
	if taken.Month() < b.Month() || (taken.Month() == b.Month() && taken.Day() < b.Day()) {
		age--
	}
	return &age
}

func uuidp(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	v := db.UUIDString(u)
	return &v
}

// GET /me/photos/v1/people?hidden=true
func (s *Server) handleListPhotoPeople(w http.ResponseWriter, r *http.Request) {
	if !s.facesReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	rows, err := s.q.ListPhotoPeople(r.Context(), db.ListPhotoPeopleParams{UserID: uid, Hidden: r.URL.Query().Get("hidden") == "true"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]personDTO, 0, len(rows))
	for _, p := range rows {
		out = append(out, personDTO{ID: db.UUIDString(p.ID), Name: p.Name, Hidden: p.Hidden, BirthDate: datep(p.BirthDate),
			Count: p.Count, Cover: uuidp(p.CoverFaceID)})
	}
	writeJSON(w, http.StatusOK, out)
}

// person reads the {id} person of the user; false: the answer was written (404).
func (s *Server) person(w http.ResponseWriter, r *http.Request, uid pgtype.UUID, raw string) (db.GetPhotoPersonRow, bool) {
	id, err := db.ParseUUID(raw)
	if err != nil {
		writeError(w, http.StatusNotFound, "person not found")
		return db.GetPhotoPersonRow{}, false
	}
	p, err := s.q.GetPhotoPerson(r.Context(), db.GetPhotoPersonParams{ID: id, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "person not found")
		return p, false
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return p, false
	}
	return p, true
}

func (s *Server) writePerson(w http.ResponseWriter, r *http.Request, uid, id pgtype.UUID) {
	p, ok := s.person(w, r, uid, db.UUIDString(id))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, personDTOOf(p))
}

func personDTOOf(p db.GetPhotoPersonRow) personDTO {
	return personDTO{ID: db.UUIDString(p.ID), Name: p.Name, Hidden: p.Hidden, BirthDate: datep(p.BirthDate),
		Count: p.Count, Cover: uuidp(p.CoverFaceID)}
}

// GET /me/photos/v1/people/{id}
func (s *Server) handleGetPhotoPerson(w http.ResponseWriter, r *http.Request) {
	if !s.facesReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	if p, ok := s.person(w, r, uid, r.PathValue("id")); ok {
		s.writePerson(w, r, uid, p.ID)
	}
}

const maxPersonName = 100

// PATCH /me/photos/v1/people/{id} {"name":string?, "hidden":bool?, "birthDate":"YYYY-MM-DD"|null?}
func (s *Server) handlePatchPhotoPerson(w http.ResponseWriter, r *http.Request) {
	if !s.facesReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	p, ok := s.person(w, r, uid, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		Name      *string         `json:"name"`
		Hidden    *bool           `json:"hidden"`
		BirthDate json.RawMessage `json:"birthDate"` // absent: unchanged; null: cleared
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	params := db.UpdatePhotoPersonParams{ID: p.ID, UserID: uid}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if utf8.RuneCountInString(name) > maxPersonName {
			writeError(w, http.StatusBadRequest, "name too long")
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if req.Hidden != nil {
		params.Hidden = pgtype.Bool{Bool: *req.Hidden, Valid: true}
	}
	if len(req.BirthDate) > 0 {
		params.SetBirthDate = true
		if string(req.BirthDate) != "null" {
			var raw string
			err := json.Unmarshal(req.BirthDate, &raw)
			var d time.Time
			if err == nil {
				d, err = time.Parse(time.DateOnly, raw)
			}
			if err != nil || d.After(time.Now()) || d.Year() < 1850 {
				writeError(w, http.StatusBadRequest, "invalid birthDate")
				return
			}
			params.BirthDate = pgtype.Date{Time: d, Valid: true}
		}
	}
	if _, err := s.q.UpdatePhotoPerson(r.Context(), params); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.writePerson(w, r, uid, p.ID)
}

// POST /me/photos/v1/people/hide {"ids":[…], "hidden":bool} — hides (or shows again) many
// people at once; the ones not the user's are skipped. Answers how many changed.
func (s *Server) handleHidePhotoPeople(w http.ResponseWriter, r *http.Request) {
	if !s.facesReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs    []string `json:"ids"`
		Hidden *bool    `json:"hidden"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Hidden == nil || len(req.IDs) == 0 || len(req.IDs) > 10000 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	ids := make([]pgtype.UUID, 0, len(req.IDs))
	for _, raw := range req.IDs {
		id, err := db.ParseUUID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid ids")
			return
		}
		ids = append(ids, id)
	}
	n, err := s.q.SetPhotoPersonsHidden(r.Context(), db.SetPhotoPersonsHiddenParams{UserID: uid, Ids: ids, Hidden: *req.Hidden})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"updated": n})
}

// POST /me/photos/v1/people/{id}/merge {"ids":[…]} — the faces of the persons ids move to
// {id} and those persons are deleted; a person without a name takes the first name among
// them.
func (s *Server) handleMergePhotoPeople(w http.ResponseWriter, r *http.Request) {
	if !s.facesReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	target, ok := s.person(w, r, uid, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	var ids []pgtype.UUID
	seen := map[pgtype.UUID]bool{}
	for _, raw := range req.IDs {
		id, err := db.ParseUUID(raw)
		if err != nil || id == target.ID {
			writeError(w, http.StatusBadRequest, "invalid ids")
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "invalid ids")
		return
	}
	ctx := r.Context()
	err := s.faces.Locked(uid, func() error {
		src, err := s.q.ListPhotoPersonsByIDs(ctx, db.ListPhotoPersonsByIDsParams{UserID: uid, Ids: ids})
		if err != nil {
			return err
		}
		if len(src) != len(ids) {
			return pgx.ErrNoRows
		}
		if target.Name == "" {
			byID := map[pgtype.UUID]string{}
			for _, p := range src {
				byID[p.ID] = p.Name
			}
			for _, id := range ids { // the order the client gave
				if n := byID[id]; n != "" {
					if _, err := s.q.UpdatePhotoPerson(ctx, db.UpdatePhotoPersonParams{ID: target.ID, UserID: uid,
						Name: pgtype.Text{String: n, Valid: true}}); err != nil {
						return err
					}
					break
				}
			}
		}
		if err := s.q.MoveFacesToPerson(ctx, db.MoveFacesToPersonParams{Target: target.ID, UserID: uid, Sources: ids}); err != nil {
			return err
		}
		return s.q.DeletePhotoPersons(ctx, db.DeletePhotoPersonsParams{UserID: uid, Ids: ids})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "person not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.writePerson(w, r, uid, target.ID)
}

// GET /me/photos/v1/people/faces/{faceId}/thumb — the square picture of one face of the
// user's, on a visible card.
func (s *Server) handlePhotoFaceThumb(w http.ResponseWriter, r *http.Request) {
	if !s.facesReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	id, err := db.ParseUUID(r.PathValue("faceId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	f, err := s.q.GetVisiblePhotoFace(r.Context(), db.GetVisiblePhotoFaceParams{ID: id, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	etag := `"f` + db.UUIDString(f.ID) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", photoCacheControl)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	a, err := s.q.GetPhotoAsset(r.Context(), db.GetPhotoAssetParams{ID: f.AssetID, UserID: uid})
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	file, err := s.thumbs.FaceThumb(r.Context(), a, db.UUIDString(f.ID), f.X1, f.Y1, f.X2, f.Y2)
	if err != nil {
		if r.Context().Err() == nil {
			log.Printf("discodrive: photos: face %s: %v", db.UUIDString(f.ID), err)
		}
		w.Header().Del("ETag")
		w.Header().Del("Cache-Control")
		writeError(w, http.StatusNotFound, "render_failed")
		return
	}
	s.serveDerivative(w, r, file, "image/jpeg")
}

type assetPersonDTO struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	FaceID string     `json:"faceId"` // their face on this photo: GET people/faces/{id}/thumb
	Age    *int       `json:"age"`    // on the day of the photo, when the birthday is known
	Face   [4]float32 `json:"face"`   // x1, y1, x2, y2 as fractions of the picture
}

// assetPeople lists the persons on the user's own card (none without people on, on the
// server or for the user).
func (s *Server) assetPeople(r *http.Request, a db.PhotoAsset) ([]assetPersonDTO, error) {
	out := []assetPersonDTO{}
	if s.faces == nil {
		return out, nil
	}
	if ps, err := s.q.GetPhotoSettings(r.Context(), a.UserID); err != nil || !ps.FacesEnabled {
		return out, nil
	}
	rows, err := s.q.ListAssetPeople(r.Context(), a.ID)
	if err != nil {
		return nil, err
	}
	seen := map[pgtype.UUID]bool{}
	for _, p := range rows {
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		out = append(out, assetPersonDTO{ID: db.UUIDString(p.ID), Name: p.Name, FaceID: db.UUIDString(p.FaceID),
			Age: ageOn(p.BirthDate, a.TakenLocal.Time), Face: [4]float32{p.X1, p.Y1, p.X2, p.Y2}})
	}
	return out, nil
}

type unknownFaceDTO struct {
	FaceID string     `json:"faceId"`
	Face   [4]float32 `json:"face"`
}

// assetUnknownFaces lists the faces on the user's own card that belong to nobody yet.
func (s *Server) assetUnknownFaces(r *http.Request, a db.PhotoAsset) ([]unknownFaceDTO, error) {
	out := []unknownFaceDTO{}
	if s.faces == nil {
		return out, nil
	}
	if ps, err := s.q.GetPhotoSettings(r.Context(), a.UserID); err != nil || !ps.FacesEnabled {
		return out, nil
	}
	rows, err := s.q.ListAssetUnknownFaces(r.Context(), a.ID)
	if err != nil {
		return nil, err
	}
	for _, f := range rows {
		out = append(out, unknownFaceDTO{FaceID: db.UUIDString(f.ID), Face: [4]float32{f.X1, f.Y1, f.X2, f.Y2}})
	}
	return out, nil
}

// faceTarget is where faces the user moves go: one of the user's people (PersonID), a new
// person of that name (Name), or nobody (Remove).
type faceTarget struct {
	PersonID string `json:"personId"`
	Name     string `json:"name"`
	Remove   bool   `json:"remove"`
}

// check answers 400 unless exactly one of the three is given, and 404 for a person not the
// user's. It returns the person (invalid for a new one or for nobody) and the trimmed name.
func (s *Server) checkFaceTarget(w http.ResponseWriter, r *http.Request, uid pgtype.UUID, t faceTarget) (pgtype.UUID, string, bool) {
	name := strings.TrimSpace(t.Name)
	given := 0
	for _, b := range []bool{t.PersonID != "", name != "", t.Remove} {
		if b {
			given++
		}
	}
	if given != 1 || utf8.RuneCountInString(name) > maxPersonName {
		writeError(w, http.StatusBadRequest, "give personId, name or remove")
		return pgtype.UUID{}, "", false
	}
	if t.PersonID == "" {
		return pgtype.UUID{}, name, true
	}
	p, ok := s.person(w, r, uid, t.PersonID)
	return p.ID, name, ok
}

// resolve creates the person of the name if one is to be made (under the user lock).
func (s *Server) resolveFaceTarget(ctx context.Context, uid, target pgtype.UUID, name string) (pgtype.UUID, error) {
	if target.Valid || name == "" {
		return target, nil
	}
	id, err := s.q.CreatePhotoPerson(ctx, uid)
	if err != nil {
		return id, err
	}
	_, err = s.q.UpdatePhotoPerson(ctx, db.UpdatePhotoPersonParams{ID: id, UserID: uid, Name: pgtype.Text{String: name, Valid: true}})
	return id, err
}

// POST /me/photos/v1/people/faces/{faceId}/assign {"personId"|"name"|"remove":true} — «Кто
// это?» and «не он»: the face goes to a person, a new one, or nobody (it shows as unknown).
func (s *Server) handleAssignPhotoFace(w http.ResponseWriter, r *http.Request) {
	if !s.facesReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	faceID, err := db.ParseUUID(r.PathValue("faceId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req faceTarget
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx := r.Context()
	if _, err := s.q.GetUserPhotoFace(ctx, db.GetUserPhotoFaceParams{ID: faceID, UserID: uid}); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	target, name, ok := s.checkFaceTarget(w, r, uid, req)
	if !ok {
		return
	}
	err = s.faces.Locked(uid, func() error {
		var err error
		if target, err = s.resolveFaceTarget(ctx, uid, target, name); err != nil {
			return err
		}
		if err := s.q.SetFacePerson(ctx, db.SetFacePersonParams{ID: faceID, UserID: uid, PersonID: target}); err != nil {
			return err
		}
		return s.q.DeleteEmptyPhotoPersons(ctx, uid)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error") // a person merged away meanwhile, at worst
		return
	}
	if !target.Valid {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	s.writePerson(w, r, uid, target)
}

// POST /me/photos/v1/people/{id}/faces/move {"assetIds":[…], "personId"|"name"|"remove":true}
// — «Это не он» from the person's grid: their faces on those cards go to another person, a
// new one, or nobody. Answers how many moved and the person they went to (null for nobody).
func (s *Server) handleMovePersonFaces(w http.ResponseWriter, r *http.Request) {
	if !s.facesReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	src, ok := s.person(w, r, uid, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		faceTarget
		AssetIDs []string `json:"assetIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.AssetIDs) == 0 || len(req.AssetIDs) > 10000 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	assets := make([]pgtype.UUID, 0, len(req.AssetIDs))
	for _, raw := range req.AssetIDs {
		id, err := db.ParseUUID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid assetIds")
			return
		}
		assets = append(assets, id)
	}
	target, name, ok := s.checkFaceTarget(w, r, uid, req.faceTarget)
	if !ok {
		return
	}
	if target == src.ID {
		writeError(w, http.StatusBadRequest, "same person")
		return
	}
	ctx := r.Context()
	var moved int64
	err := s.faces.Locked(uid, func() error {
		var err error
		if target, err = s.resolveFaceTarget(ctx, uid, target, name); err != nil {
			return err
		}
		if moved, err = s.q.MovePersonFaces(ctx, db.MovePersonFacesParams{UserID: uid, Source: src.ID, Target: target, AssetIds: assets}); err != nil {
			return err
		}
		return s.q.DeleteEmptyPhotoPersons(ctx, uid)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := map[string]any{"moved": moved, "person": nil}
	if target.Valid {
		p, err := s.q.GetPhotoPerson(ctx, db.GetPhotoPersonParams{ID: target, UserID: uid})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		out["person"] = personDTOOf(p)
	}
	writeJSON(w, http.StatusOK, out)
}

// parsePersonFilter reads ?person= for the feed: one of the user's persons, in the user's
// own feed only (with owner= or album= it is 400). Zero UUID: no filter.
func (s *Server) parsePersonFilter(w http.ResponseWriter, r *http.Request, uid pgtype.UUID) (pgtype.UUID, bool) {
	q := r.URL.Query()
	v := q.Get("person")
	if v == "" {
		return pgtype.UUID{}, true
	}
	if q.Get("owner") != "" || q.Get("album") != "" {
		writeError(w, http.StatusBadRequest, "person is for the own feed only")
		return pgtype.UUID{}, false
	}
	if s.faces == nil {
		writeError(w, http.StatusNotFound, "person not found")
		return pgtype.UUID{}, false
	}
	p, ok := s.person(w, r, uid, v)
	if !ok {
		return pgtype.UUID{}, false
	}
	return p.ID, true
}
