package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// The gallery's HTTP API. Everything lives under a versioned prefix: the web UI and a
// later mobile app both build on it.

// photosReady answers 409 photos_unavailable when this build has no media tools (lite
// image) and reports whether the caller may go on.
func (s *Server) photosReady(w http.ResponseWriter) bool {
	if s.photos == nil || s.thumbs == nil {
		writeError(w, http.StatusConflict, "photos_unavailable")
		return false
	}
	return true
}

func photosUser(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return pgtype.UUID{}, false
	}
	return uid, true
}

type photoSourceDTO struct {
	ID   string `json:"id"` // the folder's node id: what PUT settings takes back
	Name string `json:"name"`
	Path string `json:"path"`
}

type photoSettingsResponse struct {
	Available bool             `json:"available"`
	Enabled   bool             `json:"enabled"`
	Sources   []photoSourceDTO `json:"sources"`
	Indexing  struct {
		// Total counts the files read so far plus Pending: those a running full pass has
		// found and not read yet (hours of them on a first run over a big archive).
		Total   int64 `json:"total"`
		Ready   int64 `json:"ready"`
		Failed  int64 `json:"failed"`
		Pending int64 `json:"pending"`
		// Scanning: a full pass is queued or running (keep polling).
		Scanning bool `json:"scanning"`
		// FailedFiles counts the files GET /failed lists (thumbnail or metadata failed).
		FailedFiles int64 `json:"failedFiles"`
	} `json:"indexing"`
}

func (s *Server) photoSettings(r *http.Request, uid pgtype.UUID) (photoSettingsResponse, error) {
	resp := photoSettingsResponse{Available: s.photos != nil && s.thumbs != nil, Sources: []photoSourceDTO{}}
	ps, err := s.q.GetPhotoSettings(r.Context(), uid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return resp, err
	}
	resp.Enabled = ps.Enabled
	sources, err := s.q.ListPhotoSources(r.Context(), uid)
	if err != nil {
		return resp, err
	}
	for _, src := range sources {
		n, err := s.q.GetNodeAnyState(r.Context(), src.FolderNodeID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		} else if err != nil {
			return resp, err
		}
		resp.Sources = append(resp.Sources, photoSourceDTO{ID: db.UUIDString(n.ID), Name: n.Name, Path: userRelPath(db.UUIDString(uid), n.DiskPath.String)})
	}
	c, err := s.q.CountPhotoIndexing(r.Context(), uid)
	if err != nil {
		return resp, err
	}
	resp.Indexing.Total, resp.Indexing.Ready, resp.Indexing.Failed = c.Total, c.Ready, c.Failed
	resp.Indexing.FailedFiles = c.FailedFiles
	if s.photos != nil {
		resp.Indexing.Pending, resp.Indexing.Scanning = s.photos.Progress(uid)
		resp.Indexing.Total += resp.Indexing.Pending
	}
	return resp, nil
}

type photoFailedDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Folder string `json:"folder"` // the folder's path in the user's files
	Error  string `json:"error"`
}

// maxFailedPage bounds one page of GET /failed.
const maxFailedPage = 200

// GET /me/photos/v1/failed?offset=&limit= — the files of the sources that could not be
// processed (no thumbnail, or unreadable metadata), by path, at most 200 per page.
func (s *Server) handlePhotoFailed(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	limit, offset := maxFailedPage, 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v < limit {
		limit = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		offset = v
	}
	rows, err := s.q.ListPhotoFailedFiles(r.Context(), db.ListPhotoFailedFilesParams{
		UserID: uid, MaxRows: int32(limit + 1), Skip: int32(offset)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	items := make([]photoFailedDTO, 0, len(rows))
	for _, row := range rows {
		msg := row.IndexError.String
		if row.ThumbState == "failed" && row.ThumbError.String != "" {
			msg = row.ThumbError.String
		}
		if msg == "" {
			msg = "failed"
		}
		items = append(items, photoFailedDTO{ID: db.UUIDString(row.ID), Name: row.Name,
			Folder: path.Dir("/" + userRelPath(db.UUIDString(uid), row.DiskPath.String)), Error: msg})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "more": more})
}

// GET /me/photos/v1/settings
func (s *Server) handleGetPhotoSettings(w http.ResponseWriter, r *http.Request) {
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	resp, err := s.photoSettings(r, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// PUT /me/photos/v1/settings {"enabled":bool,"sourceIds":["<node id>"]}
func (s *Server) handlePutPhotoSettings(w http.ResponseWriter, r *http.Request) {
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Enabled   bool     `json:"enabled"`
		SourceIDs []string `json:"sourceIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Enabled && !s.photosReady(w) {
		return
	}
	ctx := r.Context()
	// Each source is a folder of the user's own.
	var nodes []db.Node
	seen := map[pgtype.UUID]bool{}
	for _, raw := range req.SourceIDs {
		nid, err := db.ParseUUID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid sourceIds")
			return
		}
		if seen[nid] {
			continue
		}
		seen[nid] = true
		node, err := s.q.GetNode(ctx, nid)
		if err == nil && (node.DeletedAt.Valid || node.IsVault || !node.DiskPath.Valid) {
			err = pgx.ErrNoRows
		}
		// Stage 1: only the caller's own folders (a shared one is as good as foreign).
		if err == nil && node.UserID != uid {
			err = pgx.ErrNoRows
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "folder not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !node.IsDir {
			writeError(w, http.StatusBadRequest, "sourceIds must refer to directories")
			return
		}
		nodes = append(nodes, node)
	}
	nodes = collapseNested(nodes)

	prev, err := s.q.GetPhotoSettings(ctx, uid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	old, err := s.q.ListPhotoSources(ctx, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	changed := len(old) != len(nodes)
	if !changed {
		have := map[pgtype.UUID]bool{}
		for _, o := range old {
			have[o.FolderNodeID] = true
		}
		for _, n := range nodes {
			if !have[n.ID] {
				changed = true
			}
		}
	}
	if changed {
		if err := s.q.DeletePhotoSources(ctx, uid); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for _, n := range nodes {
			if _, err := s.q.InsertPhotoSource(ctx, db.InsertPhotoSourceParams{UserID: uid, FolderNodeID: n.ID}); err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		}
	}
	if _, err := s.q.UpsertPhotoSettings(ctx, db.UpsertPhotoSettingsParams{UserID: uid, Enabled: req.Enabled}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if req.Enabled && (changed || !prev.Enabled) {
		s.reindexPhotos(uid)
	}
	resp, err := s.photoSettings(r, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// collapseNested drops every folder that lies inside another one of the list.
func collapseNested(nodes []db.Node) []db.Node {
	sort.Slice(nodes, func(i, j int) bool { return len(nodes[i].DiskPath.String) < len(nodes[j].DiskPath.String) })
	var out []db.Node
	for _, n := range nodes {
		inside := false
		for _, o := range out {
			if n.DiskPath.String == o.DiskPath.String || strings.HasPrefix(n.DiskPath.String, o.DiskPath.String+"/") {
				inside = true
				break
			}
		}
		if !inside {
			out = append(out, n)
		}
	}
	return out
}

// reindexPhotos follows a change of the sources: the change-log cursor starts at the
// present BEFORE the full pass (as for the music folder), and the pass runs in the
// background. Reindex serializes per user.
func (s *Server) reindexPhotos(uid pgtype.UUID) {
	ctx := context.Background()
	seq, err := s.q.GetUserChangeSeq(ctx, uid)
	if err == nil {
		err = s.q.SetPhotoIndexedSeq(ctx, db.SetPhotoIndexedSeqParams{UserID: uid, IndexedSeq: seq})
	}
	if err != nil {
		log.Printf("discodrive: photos cursor after a settings change: %v", err)
	}
	go func() {
		if err := s.photos.Reindex(context.Background(), uid); err != nil {
			log.Printf("discodrive: photos reindex after a settings change: %v", err)
		}
	}()
}

// ---- timeline ----

type photoRowDTO struct {
	ID          string  `json:"id"`
	TakenAt     string  `json:"takenAt"`
	TzOffsetMin *int32  `json:"tzOffsetMin"`
	Kind        string  `json:"kind"`
	W           *int32  `json:"w"`
	H           *int32  `json:"h"`
	DurationMs  *int64  `json:"durationMs"`
	Live        bool    `json:"live"`
	Edited      bool    `json:"edited"`
	Favorite    bool    `json:"favorite"`
	Thumb       string  `json:"thumb"`
	Place       *string `json:"place"`
}

// photoThumbEtag is the stable identity of an asset's thumbnail: it changes when the
// file does (node version). The feed row carries it and the file endpoints answer with
// it as the ETag.
func photoThumbEtag(a db.PhotoAsset) string {
	sum := sha256.Sum256([]byte(db.UUIDString(a.ID) + ":" + strconv.FormatInt(a.NodeVersion.Int64, 10)))
	return hex.EncodeToString(sum[:8])
}

func int4p(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	return &v.Int32
}

func textp(v pgtype.Text) *string {
	if !v.Valid || v.String == "" {
		return nil
	}
	return &v.String
}

func photoTakenAt(a db.PhotoAsset) string { return a.TakenAt.Time.UTC().Format(time.RFC3339) }

// photoChildren maps parent id -> role -> child asset for the given parents.
func (s *Server) photoChildren(r *http.Request, ids []pgtype.UUID) (map[pgtype.UUID]map[string]db.ListPhotoChildrenRow, error) {
	out := map[pgtype.UUID]map[string]db.ListPhotoChildrenRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.ListPhotoChildren(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	for _, c := range rows {
		m := out[c.ParentID]
		if m == nil {
			m = map[string]db.ListPhotoChildrenRow{}
			out[c.ParentID] = m
		}
		if _, dup := m[c.Role.String]; !dup {
			m[c.Role.String] = c
		}
	}
	return out, nil
}

// photoLang is the language place names are given in: the first one of Accept-Language,
// which the web app sets to its interface language.
func photoLang(r *http.Request) string {
	tag, _, _ := strings.Cut(r.Header.Get("Accept-Language"), ",")
	tag, _, _ = strings.Cut(tag, ";")
	return strings.TrimSpace(tag)
}

// photoPlace names an asset's place in the request's language; nil when there is none.
func (s *Server) photoPlace(r *http.Request, a db.PhotoAsset) *string {
	if n := s.photos.PlaceName(a.PlaceID, photoLang(r)); n != "" {
		return &n
	}
	return nil
}

func (s *Server) photoRows(r *http.Request, assets []db.PhotoAsset) ([]photoRowDTO, error) {
	ids := make([]pgtype.UUID, len(assets))
	for i, a := range assets {
		ids[i] = a.ID
	}
	kids, err := s.photoChildren(r, ids)
	if err != nil {
		return nil, err
	}
	rows := make([]photoRowDTO, 0, len(assets))
	for _, a := range assets {
		var dur *int64
		if a.DurationMs.Valid {
			dur = &a.DurationMs.Int64
		}
		_, live := kids[a.ID]["live-video"]
		_, edited := kids[a.ID]["original"]
		rows = append(rows, photoRowDTO{
			ID: db.UUIDString(a.ID), TakenAt: photoTakenAt(a), TzOffsetMin: int4p(a.TzOffsetMin), Kind: a.Kind,
			W: int4p(a.Width), H: int4p(a.Height), DurationMs: dur, Live: live, Edited: edited,
			Favorite: a.Favorite, Thumb: photoThumbEtag(a), Place: s.photoPlace(r, a),
		})
	}
	return rows, nil
}

// photoFilter holds the feed filters shared by buckets and months.
type photoFilter struct {
	kind     pgtype.Text
	live     pgtype.Bool
	raw      pgtype.Bool
	favorite pgtype.Bool
	source   pgtype.UUID
}

func (s *Server) parsePhotoFilter(w http.ResponseWriter, r *http.Request, uid pgtype.UUID) (photoFilter, bool) {
	var f photoFilter
	q := r.URL.Query()
	if k := q.Get("kind"); k != "" {
		if k != "photo" && k != "video" {
			writeError(w, http.StatusBadRequest, "invalid kind")
			return f, false
		}
		f.kind = pgtype.Text{String: k, Valid: true}
	}
	for name, dst := range map[string]*pgtype.Bool{"live": &f.live, "raw": &f.raw, "favorite": &f.favorite} {
		if v := q.Get(name); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid "+name)
				return f, false
			}
			*dst = pgtype.Bool{Bool: b, Valid: true}
		}
	}
	if v := q.Get("source"); v != "" {
		id, err := db.ParseUUID(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid source")
			return f, false
		}
		// Either the source row id or the folder's node id (what settings return).
		srcs, err := s.q.ListPhotoSources(r.Context(), uid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return f, false
		}
		for _, src := range srcs {
			if src.ID == id || src.FolderNodeID == id {
				f.source = src.ID
			}
		}
		if !f.source.Valid {
			writeError(w, http.StatusNotFound, "source not found")
			return f, false
		}
	}
	return f, true
}

// GET /me/photos/v1/buckets
func (s *Server) handlePhotoBuckets(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	f, ok := s.parsePhotoFilter(w, r, uid)
	if !ok {
		return
	}
	if !s.photosOn(r, uid) {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	rows, err := s.q.ListPhotoMonthBuckets(r.Context(), db.ListPhotoMonthBucketsParams{
		UserID: uid, Kind: f.kind, Live: f.live, Raw: f.raw, Favorite: f.favorite, SourceID: f.source,
	})
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

// GET /me/photos/v1/months/{yyyy-mm}
func (s *Server) handlePhotoMonth(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	month, err := time.Parse("2006-01", r.PathValue("month"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid month")
		return
	}
	f, ok := s.parsePhotoFilter(w, r, uid)
	if !ok {
		return
	}
	if !s.photosOn(r, uid) {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	assets, err := s.q.ListPhotoAssetsInMonth(r.Context(), db.ListPhotoAssetsInMonthParams{
		UserID: uid, Month: pgtype.Timestamp{Time: month, Valid: true},
		Kind: f.kind, Live: f.live, Raw: f.raw, Favorite: f.favorite, SourceID: f.source,
	})
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

// GET /me/photos/v1/on-this-day?date=MM-DD — the same day of earlier years.
func (s *Server) handlePhotoOnThisDay(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	now := time.Now()
	md := r.URL.Query().Get("date")
	if md == "" {
		md = now.Format("01-02")
	}
	// 2000 is a leap year, so 02-29 parses.
	d, err := time.Parse("2006-01-02", "2000-"+md)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid date")
		return
	}
	if !s.photosOn(r, uid) {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	assets, err := s.q.ListOnThisDay(r.Context(), db.ListOnThisDayParams{UserID: uid, Month: int32(d.Month()), Day: int32(d.Day())})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var past []db.PhotoAsset
	for _, a := range assets {
		if a.TakenLocal.Time.Year() < now.Year() {
			past = append(past, a)
		}
	}
	rows, err := s.photoRows(r, past)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	type group struct {
		Year   int           `json:"year"`
		Assets []photoRowDTO `json:"assets"`
	}
	out := []group{}
	for i, a := range past { // newest year first: the query orders by taken_local DESC
		y := a.TakenLocal.Time.Year()
		if len(out) == 0 || out[len(out)-1].Year != y {
			out = append(out, group{Year: y, Assets: []photoRowDTO{}})
		}
		out[len(out)-1].Assets = append(out[len(out)-1].Assets, rows[i])
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- card ----

// ownAsset loads the caller's asset for a read. Foreign, trashed and unknown cards are
// 404, and so is a hidden one unless the request carries a valid X-Photos-Hidden token.
func (s *Server) ownAsset(w http.ResponseWriter, r *http.Request) (db.PhotoAsset, bool) {
	return s.ownAssetState(w, r, false)
}

// ownAssetState is ownAsset that can also serve a trashed card (the gallery trash shows
// thumbnails; nothing else of a trashed card is served).
func (s *Server) ownAssetState(w http.ResponseWriter, r *http.Request, trashedOK bool) (db.PhotoAsset, bool) {
	uid, ok := photosUser(w, r)
	if !ok {
		return db.PhotoAsset{}, false
	}
	return s.loadAsset(w, r, uid, r.PathValue("id"), trashedOK)
}

// photosUnlocked reports whether the request carries a valid hidden-gallery token.
func (s *Server) photosUnlocked(r *http.Request) bool {
	tok := r.Header.Get("X-Photos-Hidden")
	return tok != "" && s.auth.ValidatePhotosHidden(r.Context(), auth.UserID(r.Context()), tok) == nil
}

func (s *Server) loadAsset(w http.ResponseWriter, r *http.Request, uid pgtype.UUID, rawID string, trashedOK bool) (db.PhotoAsset, bool) {
	id, err := db.ParseUUID(rawID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return db.PhotoAsset{}, false
	}
	a, err := s.q.GetPhotoAsset(r.Context(), db.GetPhotoAssetParams{ID: id, UserID: uid})
	if err == nil && ((a.TrashedAt.Valid && !trashedOK) || (a.Hidden && !s.photosUnlocked(r))) {
		err = pgx.ErrNoRows
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return db.PhotoAsset{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return db.PhotoAsset{}, false
	}
	return a, true
}

// GET /me/photos/v1/assets/{id}
func (s *Server) handleGetPhotoAsset(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	a, ok := s.ownAsset(w, r)
	if !ok {
		return
	}
	node, err := s.q.GetNodeAnyState(r.Context(), a.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	kids, err := s.photoChildren(r, []pgtype.UUID{a.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	row, err := s.photoRows(r, []db.PhotoAsset{a})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var liveID, origID *string
	if c, ok := kids[a.ID]["live-video"]; ok {
		v := db.UUIDString(c.ID)
		liveID = &v
	}
	if c, ok := kids[a.ID]["original"]; ok {
		v := db.UUIDString(c.ID)
		origID = &v
	}
	copies, err := s.q.CountPhotoCopies(r.Context(), db.CountPhotoCopiesParams{UserID: a.UserID, ID: a.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var lat, lon *float64
	if a.Lat.Valid && a.Lon.Valid {
		lat, lon = &a.Lat.Float64, &a.Lon.Float64
	}
	var fnum, focal *float32
	if a.FNumber.Valid {
		fnum = &a.FNumber.Float32
	}
	if a.FocalMm.Valid {
		focal = &a.FocalMm.Float32
	}
	var size *int64
	if node.Size.Valid {
		size = &node.Size.Int64
	}
	folder := path.Dir(userRelPath(db.UUIDString(a.UserID), node.DiskPath.String))
	if folder == "." {
		folder = ""
	}
	// The folder's node id opens it in Files ("where it lies"); null = the root.
	var folderID *string
	if node.ParentID.Valid {
		v := db.UUIDString(node.ParentID)
		folderID = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": row[0].ID, "kind": a.Kind, "takenAt": row[0].TakenAt, "tzOffsetMin": row[0].TzOffsetMin,
		"dateSource": a.DateSource, "w": row[0].W, "h": row[0].H, "durationMs": row[0].DurationMs,
		"videoCodec": textp(a.VideoCodec), "isHdr": a.IsHdr,
		"live": row[0].Live, "edited": row[0].Edited, "favorite": a.Favorite, "thumb": row[0].Thumb,
		// country is an ISO code: the client names it in its own language.
		"place": row[0].Place, "country": textp(a.CountryCode), "lat": lat, "lon": lon,
		"camera": map[string]any{
			"make": textp(a.Make), "model": textp(a.Model), "lens": textp(a.Lens),
			"fNumber": fnum, "focalMm": focal, "exposure": textp(a.Exposure), "iso": int4p(a.Iso),
		},
		"file":        map[string]any{"name": node.Name, "size": size, "nodeId": db.UUIDString(node.ID), "folderPath": folder, "folderId": folderID},
		"liveVideoId": liveID, "originalId": origID,
		"hidden": a.Hidden, "copies": copies,
	})
}

// photosOn reports whether the gallery is switched on for the user. A switched-off
// gallery shows empty feeds (200), even if rows from an earlier run are still indexed.
func (s *Server) photosOn(r *http.Request, uid pgtype.UUID) bool {
	ps, err := s.q.GetPhotoSettings(r.Context(), uid)
	return err == nil && ps.Enabled
}
