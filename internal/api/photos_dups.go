package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/photos"
)

// The duplicates finder: groups of near-identical cards, resolving a group (keep one,
// the rest to the trash) and dismissing one («Оставить все»).

type dupAssetDTO struct {
	photoRowDTO
	Name   string `json:"name"`
	Path   string `json:"path"`   // the file's folder, relative to the user's root ("" = root)
	Format string `json:"format"` // the extension, upper case, no dot
	Bytes  int64  `json:"bytes"`  // every byte copy of the content together
	Copies int    `json:"copies"` // byte copies, the card itself included
}

type dupGroupDTO struct {
	Assets []dupAssetDTO `json:"assets"` // [0] is the suggested keep
}

type dupProgressDTO struct {
	Hashed   int64 `json:"hashed"`   // cards with a thumbnail that are hashed
	Total    int64 `json:"total"`    // cards with a thumbnail
	Scanning bool  `json:"scanning"` // the hash backfill is still going through the library
}

// userLocks hands out one mutex per user: a resolve or dismiss checks the group and acts
// on it under the lock, so two requests about the same cards cannot both pass the check
// (keep A/delete B and keep B/delete A at once would trash both). One server process.
type userLocks struct {
	mu sync.Mutex
	m  map[pgtype.UUID]*sync.Mutex
}

func (l *userLocks) lock(uid pgtype.UUID) func() {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[pgtype.UUID]*sync.Mutex{}
	}
	m, ok := l.m[uid]
	if !ok {
		m = &sync.Mutex{}
		l.m[uid] = m
	}
	l.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// GET /me/photos/v1/duplicates → {"groups":[{"assets":[...]}], "reclaimBytes", "progress"}
func (s *Server) handlePhotoDuplicates(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	groups, err := photos.LoadDuplicateGroups(r.Context(), s.q, uid)
	if err != nil {
		log.Printf("discodrive: photo duplicates: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var ids []pgtype.UUID
	for _, g := range groups {
		for _, c := range g {
			ids = append(ids, c.ID)
		}
	}
	byID := map[pgtype.UUID]photoRowDTO{}
	if len(ids) > 0 {
		assets, err := s.q.ListOwnPhotoAssets(r.Context(), db.ListOwnPhotoAssetsParams{UserID: uid, Ids: ids})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		rows, err := s.photoRows(r, assets)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for i, a := range assets {
			if a.Hidden || a.TrashedAt.Valid {
				continue // hidden or trashed between the two reads
			}
			byID[a.ID] = rows[i]
		}
	}
	user := db.UUIDString(uid)
	out := struct {
		Groups       []dupGroupDTO  `json:"groups"`
		ReclaimBytes int64          `json:"reclaimBytes"`
		Progress     dupProgressDTO `json:"progress"`
	}{Groups: []dupGroupDTO{}}
	for _, g := range groups {
		dg := dupGroupDTO{Assets: make([]dupAssetDTO, 0, len(g))}
		for _, c := range g {
			row, found := byID[c.ID]
			if !found {
				continue // gone between the two reads
			}
			dg.Assets = append(dg.Assets, dupAssetDTO{
				photoRowDTO: row, Name: c.Name, Path: dupFolder(user, c.DiskPath),
				Format: strings.ToUpper(strings.TrimPrefix(path.Ext(c.Name), ".")),
				Bytes:  c.Bytes, Copies: c.Copies,
			})
		}
		if len(dg.Assets) >= 2 {
			out.Groups = append(out.Groups, dg)
			for _, a := range dg.Assets[1:] { // what goes if the suggestion is taken
				out.ReclaimBytes += a.Bytes
			}
		}
	}
	p, err := s.q.CountPhotoHashProgress(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out.Progress = dupProgressDTO{Hashed: p.Hashed, Total: p.Total, Scanning: s.thumbs != nil && s.thumbs.Backfilling()}
	writeJSON(w, http.StatusOK, out)
}

// dupFolder is the folder of a file's disk path, relative to the user's root.
func dupFolder(userID, diskPath string) string {
	dir := path.Dir(userRelPath(userID, diskPath))
	if dir == "." || dir == "/" || dir == userID {
		return ""
	}
	return dir
}

// dupGroupOf checks that ids are own live cards (404 otherwise, as ownCards) that are
// duplicates of one another right now, as the members of one duplicate group are, each
// content named once (400 otherwise; a byte copy of a card stands for that card). The
// check reads these cards only (photos.MutualDuplicates), not the whole library. It returns the ids with their content keys, in request
// order, and answers the request itself when the check fails. Call it under dupLocks.
func (s *Server) dupGroupOf(w http.ResponseWriter, r *http.Request, uid pgtype.UUID, raw []string) ([]pgtype.UUID, []string, bool) {
	ids, ok := s.ownCards(w, r, uid, raw, false)
	if !ok {
		return nil, nil, false
	}
	assets, err := s.q.ListOwnPhotoAssets(r.Context(), db.ListOwnPhotoAssetsParams{UserID: uid, Ids: ids})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return nil, nil, false
	}
	keyOf := make(map[pgtype.UUID]string, len(assets))
	for _, a := range assets {
		keyOf[a.ID] = a.ContentHash.String
		if !a.ContentHash.Valid {
			keyOf[a.ID] = db.UUIDString(a.ID)
		}
	}
	keys := make([]string, len(ids))
	seen := make(map[string]bool, len(ids))
	for i, id := range ids {
		k, found := keyOf[id]
		if !found {
			writeError(w, http.StatusNotFound, "not found")
			return nil, nil, false
		}
		if seen[k] {
			writeError(w, http.StatusBadRequest, "repeated id")
			return nil, nil, false
		}
		seen[k] = true
		keys[i] = k
	}
	together, err := photos.MutualDuplicates(r.Context(), s.q, uid, keys)
	if err != nil {
		log.Printf("discodrive: photo duplicates: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return nil, nil, false
	}
	if together {
		return ids, keys, true
	}
	writeError(w, http.StatusBadRequest, "not one duplicate group")
	return nil, nil, false
}

// POST /me/photos/v1/duplicates/resolve {"keep":id, "delete":[id,...]} → {"deletedNodes":n}
// Copies favorite and album membership onto keep, then sends the delete cards (with their
// byte copies and children) to the trash. Every id must be in the same current group.
func (s *Server) handleResolvePhotoDuplicates(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	uid, ok := photosUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Keep   string   `json:"keep"`
		Delete []string `json:"delete"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Keep == "" || len(req.Delete) == 0 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if _, err := db.ParseUUID(req.Keep); err != nil {
		writeError(w, http.StatusBadRequest, "invalid ids")
		return
	}
	for _, d := range req.Delete {
		if d == req.Keep {
			writeError(w, http.StatusBadRequest, "keep is among delete")
			return
		}
	}
	defer s.dupLocks.lock(uid)() // from the check through the trash
	ids, _, ok := s.dupGroupOf(w, r, uid, append([]string{req.Keep}, req.Delete...))
	if !ok {
		return
	}
	keep, del := ids[0], ids[1:] // distinct contents: dupGroupOf refuses a repeated one

	// Favorite and albums of every card that goes, byte copies included: those go too.
	copies, err := s.q.ListPhotoCardCopies(r.Context(), db.ListPhotoCardCopiesParams{UserID: uid, Ids: ids})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	from := append([]pgtype.UUID{}, del...)
	all := append([]pgtype.UUID{}, ids...)
	for _, c := range copies {
		all = append(all, c.CopyID)
		if c.CardID != keep {
			from = append(from, c.CopyID)
		}
	}
	cards, err := s.q.ListOwnPhotoAssets(r.Context(), db.ListOwnPhotoAssetsParams{UserID: uid, Ids: all})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	favorite := false
	for _, a := range cards {
		favorite = favorite || a.Favorite
	}
	err = s.q.InTx(r.Context(), func(q *db.Queries) error {
		if favorite {
			// The kept card, its children and its byte copies, as PATCH does.
			if err := q.SetPhotoCardFlags(r.Context(), db.SetPhotoCardFlagsParams{
				UserID: uid, Ids: []pgtype.UUID{keep}, Favorite: pgtype.Bool{Bool: true, Valid: true}}); err != nil {
				return err
			}
		}
		albums, err := q.CopyPhotoAlbumItems(r.Context(), db.CopyPhotoAlbumItemsParams{Keep: keep, FromIds: from})
		if err != nil {
			return err
		}
		for _, al := range albums { // as adding to an album does: it moves up the list
			if err := q.TouchPhotoAlbum(r.Context(), al); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("discodrive: photo duplicates resolve: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	deleted, err := s.trashPhotoCards(r.Context(), uid, del)
	if errors.Is(err, errPhotoNodeList) {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deletedNodes": deleted})
}

// POST /me/photos/v1/duplicates/dismiss {"ids":[id,...]} → 204. «Оставить все»: the
// group stops being offered; a new copy of the frame brings it back. The pairs are kept by
// content, so a byte copy taking over as a card's representative changes nothing.
func (s *Server) handleDismissPhotoDuplicates(w http.ResponseWriter, r *http.Request) {
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
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) < 2 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	defer s.dupLocks.lock(uid)()
	_, keys, ok := s.dupGroupOf(w, r, uid, req.IDs)
	if !ok {
		return
	}
	err := s.q.InTx(r.Context(), func(q *db.Queries) error {
		for i := range keys {
			for j := i + 1; j < len(keys); j++ {
				p := photos.MakeDupPair(keys[i], keys[j])
				if err := q.AddDupDismissed(r.Context(), db.AddDupDismissedParams{UserID: uid, A: p[0], B: p[1]}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("discodrive: photo duplicates dismiss: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
