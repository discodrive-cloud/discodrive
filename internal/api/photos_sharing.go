package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// Gallery sharing (.local/spec-photos-stage3.md): a partner sees the owner's whole gallery,
// read-only; an album gathers cards its members can see; a link opens an album to people
// without an account (photos_public.go).

const (
	maxAlbumName  = 200
	maxAlbumBatch = 5000
)

// photoScope is whose cards a feed request shows: the viewer's own gallery with the
// partners they mix in, one partner's gallery (?owner=), or an album (?album=).
type photoScope struct {
	owners []pgtype.UUID
	album  pgtype.UUID
}

func (s *Server) photoScope(w http.ResponseWriter, r *http.Request, uid pgtype.UUID) (photoScope, bool) {
	q := r.URL.Query()
	if v := q.Get("album"); v != "" {
		id, role, ok := s.albumRole(w, r, uid, v)
		if !ok {
			return photoScope{}, false
		}
		if role == "" {
			writeError(w, http.StatusNotFound, "album not found")
			return photoScope{}, false
		}
		return photoScope{album: id}, true
	}
	if v := q.Get("owner"); v != "" && v != db.UUIDString(uid) {
		owner, err := db.ParseUUID(v)
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return photoScope{}, false
		}
		ok, err := s.q.IsPhotoPartner(r.Context(), db.IsPhotoPartnerParams{OwnerID: owner, ViewerID: uid})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return photoScope{}, false
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not found")
			return photoScope{}, false
		}
		if !s.photosOn(r, owner) {
			return photoScope{}, true
		}
		return photoScope{owners: []pgtype.UUID{owner}}, true
	}
	var sc photoScope
	if s.photosOn(r, uid) {
		sc.owners = append(sc.owners, uid)
	}
	if q.Get("area") != "" {
		return sc, true // the map is one's own: so is the grid of its area
	}
	partners, err := s.q.ListPhotoPartnerOwners(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return photoScope{}, false
	}
	for _, p := range partners {
		if p.InFeed && s.photosOn(r, p.ID) {
			sc.owners = append(sc.owners, p.ID)
		}
	}
	return sc, true
}

// viewAsset loads the card named in the path: the user's own (under the usual rules) or
// one somebody shares with them. own reports which.
func (s *Server) viewAsset(w http.ResponseWriter, r *http.Request, trashedOK bool) (a db.PhotoAsset, own, ok bool) {
	uid, ok := photosUser(w, r)
	if !ok {
		return db.PhotoAsset{}, false, false
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return db.PhotoAsset{}, false, false
	}
	_, err = s.q.GetPhotoAsset(r.Context(), db.GetPhotoAssetParams{ID: id, UserID: uid})
	if err == nil {
		a, ok := s.loadAsset(w, r, uid, r.PathValue("id"), trashedOK)
		return a, true, ok
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "internal error")
		return db.PhotoAsset{}, false, false
	}
	a, err = s.q.GetViewablePhotoAsset(r.Context(), db.GetViewablePhotoAssetParams{ID: id, ViewerID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return db.PhotoAsset{}, false, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return db.PhotoAsset{}, false, false
	}
	return a, false, true
}

// GET /me/photos/v1/assets/{id}/original — the file of any card the user can see.
func (s *Server) handlePhotoOriginal(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	a, _, ok := s.viewAsset(w, r, false)
	if !ok {
		return
	}
	s.serveOriginal(w, r, a, true)
}

// serveOriginal sends the card's file: as a download, or inline for a <video> element.
func (s *Server) serveOriginal(w http.ResponseWriter, r *http.Request, a db.PhotoAsset, download bool) {
	node, err := s.q.GetNodeAnyState(r.Context(), a.NodeID)
	if err != nil || node.IsDir || !node.DiskPath.Valid || node.DeletedAt.Valid {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	headers := func() {
		if download {
			setDownloadHeaders(w, node.Mime, node.Name)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", node.Mime.String)
		w.Header().Set("Cache-Control", "private, no-store")
	}
	if s.xaccel {
		headers()
		w.Header().Set("X-Accel-Redirect", xaccelRedirect(node.DiskPath.String))
		return
	}
	s.serveFileContent(w, r, node.Name, node.DiskPath.String, headers)
}

// ---- partners ----

type photoPartnerDTO struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	InFeed *bool  `json:"inFeed,omitempty"`
}

// GET /me/photos/v1/partners
func (s *Server) handlePhotoPartners(w http.ResponseWriter, r *http.Request) {
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	viewers, err := s.q.ListPhotoPartnerViewers(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	owners, err := s.q.ListPhotoPartnerOwners(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := map[string][]photoPartnerDTO{"sharing": {}, "sharedWithMe": {}}
	for _, v := range viewers {
		out["sharing"] = append(out["sharing"], photoPartnerDTO{UserID: db.UUIDString(v.ID), Email: v.Email})
	}
	for _, o := range owners {
		in := o.InFeed
		out["sharedWithMe"] = append(out["sharedWithMe"], photoPartnerDTO{UserID: db.UUIDString(o.ID), Email: o.Email, InFeed: &in})
	}
	writeJSON(w, http.StatusOK, out)
}

// userByEmail finds another user of the server for sharing with. The answer for an unknown
// address is the same 404 as for one's own.
func (s *Server) userByEmail(w http.ResponseWriter, r *http.Request, uid pgtype.UUID, email string) (pgtype.UUID, bool) {
	email = strings.TrimSpace(email)
	if email == "" {
		writeError(w, http.StatusBadRequest, "email required")
		return pgtype.UUID{}, false
	}
	u, err := s.q.GetUserByEmail(r.Context(), email)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && u.ID == uid) {
		writeError(w, http.StatusNotFound, "user not found")
		return pgtype.UUID{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return pgtype.UUID{}, false
	}
	return u.ID, true
}

// POST /me/photos/v1/partners {email} — open my gallery to them.
func (s *Server) handleAddPhotoPartner(w http.ResponseWriter, r *http.Request) {
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	viewer, ok := s.userByEmail(w, r, uid, req.Email)
	if !ok {
		return
	}
	if err := s.q.AddPhotoPartner(r.Context(), db.AddPhotoPartnerParams{OwnerID: uid, ViewerID: viewer}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, photoPartnerDTO{UserID: db.UUIDString(viewer), Email: strings.TrimSpace(req.Email)})
}

// DELETE /me/photos/v1/partners/{userId} — close my gallery to them.
// DELETE /me/photos/v1/partners/{userId}/received — stop seeing theirs.
func (s *Server) handleDeletePhotoPartner(received bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := photosUser(w, r)
		if !ok {
			return
		}
		other, err := db.ParseUUID(r.PathValue("userId"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		p := db.DeletePhotoPartnerParams{OwnerID: uid, ViewerID: other}
		if received {
			p = db.DeletePhotoPartnerParams{OwnerID: other, ViewerID: uid}
		}
		n, err := s.q.DeletePhotoPartner(r.Context(), p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if n == 0 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// PUT /me/photos/v1/partners/{userId}/feed {inFeed}
func (s *Server) handlePhotoPartnerFeed(w http.ResponseWriter, r *http.Request) {
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	owner, err := db.ParseUUID(r.PathValue("userId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		InFeed bool `json:"inFeed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	n, err := s.q.SetPhotoPartnerFeed(r.Context(), db.SetPhotoPartnerFeedParams{OwnerID: owner, ViewerID: uid, InFeed: req.InFeed})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- albums ----

// albumRole parses an album id and tells the user's place in it: "owner", "member" or ""
// (not theirs, or no such album).
func (s *Server) albumRole(w http.ResponseWriter, r *http.Request, uid pgtype.UUID, raw string) (pgtype.UUID, string, bool) {
	id, err := db.ParseUUID(raw)
	if err != nil {
		return id, "", true
	}
	role, err := s.q.GetPhotoAlbumRole(r.Context(), db.GetPhotoAlbumRoleParams{ID: id, UserID: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return id, "", true
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return id, "", false
	}
	return id, role, true
}

// album resolves {id} for a handler: 404 unless the user is in the album, 403 when the
// action needs the owner.
func (s *Server) album(w http.ResponseWriter, r *http.Request, ownerOnly bool) (pgtype.UUID, pgtype.UUID, string, bool) {
	uid, ok := photosUser(w, r)
	if !ok {
		return uid, pgtype.UUID{}, "", false
	}
	id, role, ok := s.albumRole(w, r, uid, r.PathValue("id"))
	if !ok {
		return uid, id, "", false
	}
	if role == "" {
		writeError(w, http.StatusNotFound, "album not found")
		return uid, id, "", false
	}
	if ownerOnly && role != "owner" {
		writeError(w, http.StatusForbidden, "only the album's owner can do that")
		return uid, id, role, false
	}
	return uid, id, role, true
}

func albumName(w http.ResponseWriter, raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxAlbumName || strings.ContainsAny(name, "\x00\r\n") {
		writeError(w, http.StatusBadRequest, "invalid album name")
		return "", false
	}
	return name, true
}

type photoAlbumDTO struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Own        bool         `json:"own"`
	OwnerEmail string       `json:"ownerEmail"`
	Count      int64        `json:"count"`
	Members    int64        `json:"members"`
	Cover      *photoRowDTO `json:"cover"`
	UpdatedAt  string       `json:"updatedAt"`
}

// GET /me/photos/v1/albums
func (s *Server) handlePhotoAlbums(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	albums, err := s.q.ListPhotoAlbumsFor(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var coverIDs []pgtype.UUID
	for _, a := range albums {
		if a.CoverID.Valid {
			coverIDs = append(coverIDs, a.CoverID)
		}
	}
	covers := map[pgtype.UUID]photoRowDTO{}
	if len(coverIDs) > 0 {
		assets, err := s.q.ListPhotoAssetsByIDs(r.Context(), coverIDs)
		if err == nil {
			var rows []photoRowDTO
			rows, err = s.photoRows(r, assets)
			for i, row := range rows {
				covers[assets[i].ID] = row
			}
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	out := make([]photoAlbumDTO, 0, len(albums))
	for _, a := range albums {
		d := photoAlbumDTO{ID: db.UUIDString(a.ID), Name: a.Name, Own: a.OwnerID == uid, OwnerEmail: a.OwnerEmail,
			Count: a.Count, Members: a.Members, UpdatedAt: a.UpdatedAt.Time.UTC().Format(time.RFC3339)}
		if c, ok := covers[a.CoverID]; ok {
			d.Cover = &c
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /me/photos/v1/albums {name, assetIds?}
func (s *Server) handleCreatePhotoAlbum(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     string   `json:"name"`
		AssetIDs []string `json:"assetIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	name, ok := albumName(w, req.Name)
	if !ok {
		return
	}
	ids, ok := s.seenCards(w, r, uid, req.AssetIDs)
	if !ok {
		return
	}
	al, err := s.q.CreatePhotoAlbum(r.Context(), db.CreatePhotoAlbumParams{OwnerID: uid, Name: name})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(ids) > 0 {
		if _, err := s.q.AddPhotoAlbumItems(r.Context(), db.AddPhotoAlbumItemsParams{AlbumID: al.ID, AssetIds: ids, AddedBy: uid}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": db.UUIDString(al.ID), "name": al.Name, "added": len(ids)})
}

// seenCards checks a batch of card ids the user wants in an album: each has to be a card
// (not a part of one) the user can see — their own, a partner's, or already in the album.
func (s *Server) seenCards(w http.ResponseWriter, r *http.Request, uid pgtype.UUID, raw []string) ([]pgtype.UUID, bool) {
	if len(raw) > maxAlbumBatch {
		writeError(w, http.StatusBadRequest, "too many photos at once")
		return nil, false
	}
	var ids []pgtype.UUID
	seen := map[pgtype.UUID]bool{}
	for _, v := range raw {
		id, err := db.ParseUUID(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid asset id")
			return nil, false
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		a, err := s.q.GetPhotoAsset(r.Context(), db.GetPhotoAssetParams{ID: id, UserID: uid})
		if errors.Is(err, pgx.ErrNoRows) {
			a, err = s.q.GetViewablePhotoAsset(r.Context(), db.GetViewablePhotoAssetParams{ID: id, ViewerID: uid})
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "photo not found")
			return nil, false
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return nil, false
		}
		if a.ParentID.Valid || a.TrashedAt.Valid {
			writeError(w, http.StatusBadRequest, "not a gallery card")
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// GET /me/photos/v1/albums/{id}
func (s *Server) handleGetPhotoAlbum(w http.ResponseWriter, r *http.Request) {
	uid, id, role, ok := s.album(w, r, false)
	if !ok {
		return
	}
	al, err := s.q.GetPhotoAlbum(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	owner, err := s.q.GetUserByID(r.Context(), al.OwnerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	members, err := s.q.ListPhotoAlbumMembers(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	ms := make([]photoPartnerDTO, 0, len(members))
	for _, m := range members {
		ms = append(ms, photoPartnerDTO{UserID: db.UUIDString(m.ID), Email: m.Email})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": db.UUIDString(al.ID), "name": al.Name, "own": role == "owner", "ownerEmail": owner.Email,
		"members": ms, "me": db.UUIDString(uid),
	})
}

// PATCH /me/photos/v1/albums/{id} {name}
func (s *Server) handleRenamePhotoAlbum(w http.ResponseWriter, r *http.Request) {
	_, id, _, ok := s.album(w, r, true)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	name, ok := albumName(w, req.Name)
	if !ok {
		return
	}
	if _, err := s.q.RenamePhotoAlbum(r.Context(), db.RenamePhotoAlbumParams{ID: id, Name: name}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /me/photos/v1/albums/{id} — the owner deletes it, a member leaves it. The photos
// themselves stay where they are.
func (s *Server) handleDeletePhotoAlbum(w http.ResponseWriter, r *http.Request) {
	uid, id, role, ok := s.album(w, r, false)
	if !ok {
		return
	}
	var err error
	if role == "owner" {
		_, err = s.q.DeletePhotoAlbum(r.Context(), id)
	} else {
		_, err = s.q.DeletePhotoAlbumMember(r.Context(), db.DeletePhotoAlbumMemberParams{AlbumID: id, UserID: uid})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type albumItemsRequest struct {
	AssetIDs []string `json:"assetIds"`
}

// POST /me/photos/v1/albums/{id}/items {assetIds}
func (s *Server) handleAddPhotoAlbumItems(w http.ResponseWriter, r *http.Request) {
	uid, id, _, ok := s.album(w, r, false)
	if !ok {
		return
	}
	var req albumItemsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ids, ok := s.seenCards(w, r, uid, req.AssetIDs)
	if !ok {
		return
	}
	n, err := s.q.AddPhotoAlbumItems(r.Context(), db.AddPhotoAlbumItemsParams{AlbumID: id, AssetIds: ids, AddedBy: uid})
	if err == nil && n > 0 {
		err = s.q.TouchPhotoAlbum(r.Context(), id)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": n})
}

// POST /me/photos/v1/albums/{id}/items/remove {assetIds} — the owner takes out anything,
// a member what they put in.
func (s *Server) handleRemovePhotoAlbumItems(w http.ResponseWriter, r *http.Request) {
	uid, id, role, ok := s.album(w, r, false)
	if !ok {
		return
	}
	var req albumItemsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(req.AssetIDs) > maxAlbumBatch {
		writeError(w, http.StatusBadRequest, "too many photos at once")
		return
	}
	ids := make([]pgtype.UUID, 0, len(req.AssetIDs))
	for _, v := range req.AssetIDs {
		a, err := db.ParseUUID(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid asset id")
			return
		}
		ids = append(ids, a)
	}
	n, err := s.q.RemovePhotoAlbumItems(r.Context(), db.RemovePhotoAlbumItemsParams{
		AlbumID: id, AssetIds: ids, AnyItem: role == "owner", UserID: uid})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": n, "kept": int64(len(ids)) - n})
}

// POST /me/photos/v1/albums/{id}/members {email}
func (s *Server) handleAddPhotoAlbumMember(w http.ResponseWriter, r *http.Request) {
	uid, id, _, ok := s.album(w, r, true)
	if !ok {
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	user, ok := s.userByEmail(w, r, uid, req.Email)
	if !ok {
		return
	}
	if err := s.q.AddPhotoAlbumMember(r.Context(), db.AddPhotoAlbumMemberParams{AlbumID: id, UserID: user}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, photoPartnerDTO{UserID: db.UUIDString(user), Email: strings.TrimSpace(req.Email)})
}

// DELETE /me/photos/v1/albums/{id}/members/{userId} — the owner removes a member; a member
// can remove themselves.
func (s *Server) handleDeletePhotoAlbumMember(w http.ResponseWriter, r *http.Request) {
	uid, id, role, ok := s.album(w, r, false)
	if !ok {
		return
	}
	user, err := db.ParseUUID(r.PathValue("userId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if role != "owner" && user != uid {
		writeError(w, http.StatusForbidden, "only the album's owner can do that")
		return
	}
	n, err := s.q.DeletePhotoAlbumMember(r.Context(), db.DeletePhotoAlbumMemberParams{AlbumID: id, UserID: user})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- album links ----

type albumLinkDTO struct {
	ID            string  `json:"id"`
	Token         string  `json:"token"`
	URL           string  `json:"url"`
	HasPassword   bool    `json:"hasPassword"`
	ExpiresAt     *string `json:"expiresAt"`
	AllowDownload bool    `json:"allowDownload"`
	CreatedAt     string  `json:"createdAt"`
}

func albumLinkOut(l db.PhotoAlbumLink) albumLinkDTO {
	d := albumLinkDTO{ID: db.UUIDString(l.ID), Token: l.Token, URL: "/app/a/" + l.Token, HasPassword: l.PasswordHash.Valid,
		AllowDownload: l.AllowDownload, CreatedAt: l.CreatedAt.Time.UTC().Format(time.RFC3339)}
	if l.ExpiresAt.Valid {
		v := l.ExpiresAt.Time.UTC().Format(time.RFC3339)
		d.ExpiresAt = &v
	}
	return d
}

// GET /me/photos/v1/albums/{id}/links
func (s *Server) handlePhotoAlbumLinks(w http.ResponseWriter, r *http.Request) {
	_, id, _, ok := s.album(w, r, true)
	if !ok {
		return
	}
	links, err := s.q.ListPhotoAlbumLinks(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]albumLinkDTO, 0, len(links))
	for _, l := range links {
		out = append(out, albumLinkOut(l))
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /me/photos/v1/albums/{id}/links {password?, expiresAt?, allowDownload}
func (s *Server) handleCreatePhotoAlbumLink(w http.ResponseWriter, r *http.Request) {
	_, id, _, ok := s.album(w, r, true)
	if !ok {
		return
	}
	var req struct {
		Password      string     `json:"password"`
		ExpiresAt     *time.Time `json:"expiresAt"`
		AllowDownload bool       `json:"allowDownload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		writeError(w, http.StatusBadRequest, "expiry in the past")
		return
	}
	p := db.CreatePhotoAlbumLinkParams{AlbumID: id, AllowDownload: req.AllowDownload}
	if req.ExpiresAt != nil {
		p.ExpiresAt = pgtype.Timestamptz{Time: *req.ExpiresAt, Valid: true}
	}
	if req.Password != "" {
		if utf8.RuneCountInString(req.Password) > 256 {
			writeError(w, http.StatusBadRequest, "password too long")
			return
		}
		h, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		p.PasswordHash = pgtype.Text{String: h, Valid: true}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	p.Token = base64.RawURLEncoding.EncodeToString(buf)
	l, err := s.q.CreatePhotoAlbumLink(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, albumLinkOut(l))
}

// DELETE /me/photos/v1/albums/{id}/links/{linkId}
func (s *Server) handleDeletePhotoAlbumLink(w http.ResponseWriter, r *http.Request) {
	_, id, _, ok := s.album(w, r, true)
	if !ok {
		return
	}
	link, err := db.ParseUUID(r.PathValue("linkId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	n, err := s.q.DeletePhotoAlbumLink(r.Context(), db.DeletePhotoAlbumLinkParams{ID: link, AlbumID: id})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
