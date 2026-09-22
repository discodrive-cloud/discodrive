package api

import (
	"mime"
	"net/http"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/music"
	"discodrive/internal/storage"
)

// mediaItemDTO is one playable file in a folder, ready for the player: identity,
// a pre-minted stream URL and whatever metadata the music index has for it.
type mediaItemDTO struct {
	NodeID    string `json:"node_id"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	Size      *int64 `json:"size,omitempty"`
	Version   int64  `json:"version"`
	StreamURL string `json:"stream_url"`
	// Indexed reports whether the tags below came from the music index. Non-indexed
	// items get them lazily: the single-node variant of this endpoint reads the file's
	// tags on the fly when the track starts playing.
	Indexed  bool   `json:"indexed"`
	Title    string `json:"title,omitempty"`
	Artist   string `json:"artist,omitempty"`
	Album    string `json:"album,omitempty"`
	Track    *int32 `json:"track,omitempty"`
	Disc     *int32 `json:"disc,omitempty"`
	Duration *int32 `json:"duration,omitempty"` // seconds; absent for non-indexed files (probing is too expensive per request)
	Bitrate  *int32 `json:"bitrate,omitempty"`
}

// GET /files/{id}/media — the player's view of a folder: playable files only
// (media mime, not a dir, not an encrypted vault entry), each with a stream URL.
// GET /files/media — the same for the root of the user's own tree (no node id).
// The ?node_id=X variant returns a single child of that folder: used to refresh
// an expired stream URL and to lazily read tags of non-indexed files; this variant
// also serves as the player's error probe (alive → new URL, 404 → skip the track).
func (s *Server) handleMediaListing(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	parentID := r.PathValue("id")

	mint, err := s.auth.StreamMinter(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if nodeID := r.URL.Query().Get("node_id"); nodeID != "" {
		// Root children have no parent: UUIDString of a NULL parent_id is "",
		// so the containment check in mediaSingle holds for both variants.
		s.mediaSingle(w, r, userID, parentID, nodeID, mint)
		return
	}

	var children []db.Node
	if parentID == "" {
		children, err = s.files.RootChildren(r.Context(), userID)
	} else {
		children, err = s.files.ListChildren(r.Context(), userID, parentID)
	}
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	playable := make([]db.Node, 0, len(children))
	ids := make([]pgtype.UUID, 0, len(children))
	for _, n := range children {
		if isPlayableNode(n) {
			playable = append(playable, n)
			ids = append(ids, n.ID)
		}
	}
	meta, err := s.songMeta(r, ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	items := make([]mediaItemDTO, 0, len(playable))
	for _, n := range playable {
		item, err := buildMediaItem(n, mint, meta[db.UUIDString(n.ID)])
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// mediaSingle serves the ?node_id= variant: one access-checked child of parent.
func (s *Server) mediaSingle(w http.ResponseWriter, r *http.Request, userID, parentID, nodeID string, mint func(string) (string, error)) {
	node, err := s.files.NodeForDownload(r.Context(), userID, nodeID)
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	// The node must actually live in the folder the URL names: keeps the contract
	// honest and stops the parent path segment from becoming decorative.
	if db.UUIDString(node.ParentID) != parentID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !isPlayableNode(node) {
		writeError(w, http.StatusForbidden, "not a streamable media type")
		return
	}
	meta, err := s.songMeta(r, []pgtype.UUID{node.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	item, err := buildMediaItem(node, mint, meta[db.UUIDString(node.ID)])
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Not in the music index → read tags from the file now (single node, cheap).
	// Audio formats the scanner understands only; video gets name+mime and no more.
	if !item.Indexed && music.IsAudioFile(node.Name) {
		if m, err := music.ReadStoredMeta(s.storageRoot, filepath.Join(s.storageRoot, node.DiskPath.String)); err == nil {
			item.Title, item.Artist, item.Album = m.Title, m.Artist, m.Album
			if m.Track > 0 {
				tr := int32(m.Track)
				item.Track = &tr
			}
			if m.Disc > 0 {
				d := int32(m.Disc)
				item.Disc = &d
			}
		}
	}
	writeJSON(w, http.StatusOK, item)
}

// songMeta fetches index metadata for a set of nodes in one query and keys it by node ID.
func (s *Server) songMeta(r *http.Request, ids []pgtype.UUID) (map[string]db.SongsMetaByNodeIDsRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.q.SongsMetaByNodeIDs(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	m := make(map[string]db.SongsMetaByNodeIDsRow, len(rows))
	for _, row := range rows {
		m[db.UUIDString(row.NodeID)] = row
	}
	return m, nil
}

// GET /files/{id}/media-cover — cover art for a playable file: the embedded tag
// picture, else a sibling cover.jpg/png. Served same-origin so the SPA fetches it
// as a blob (a plain <img src> cannot carry the Bearer header). Unlike the tag
// editor's cover route this has no music-folder restriction: the player works on
// arbitrary folders.
func (s *Server) handleMediaCover(w http.ResponseWriter, r *http.Request) {
	node, err := s.files.NodeForDownload(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		writeStorageErr(w, err)
		return
	}
	if !isPlayableNode(node) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	abs := filepath.Join(s.storageRoot, node.DiskPath.String)
	if data, ct, ok := music.EmbeddedStoredCover(s.storageRoot, abs); ok {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(data)
		return
	}
	if p, ok := music.ResolveCoverPath(filepath.Dir(abs)); ok {
		f, err := storage.NewLocalDisk(s.storageRoot).OpenAbsolute(p)
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		ct := mime.TypeByExtension(filepath.Ext(p))
		if ct == "" {
			ct = "image/jpeg"
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", ct)
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
		return
	}
	writeError(w, http.StatusNotFound, "not found")
}

// isPlayableNode: a real file, not an encrypted vault entry, with a mime the
// stream endpoint will actually serve.
func isPlayableNode(n db.Node) bool {
	return !n.IsDir && !n.IsVault && n.Mime.Valid && streamableMime[n.Mime.String]
}

func buildMediaItem(n db.Node, mint func(string) (string, error), song db.SongsMetaByNodeIDsRow) (mediaItemDTO, error) {
	nid := db.UUIDString(n.ID)
	tok, err := mint(nid)
	if err != nil {
		return mediaItemDTO{}, err
	}
	item := mediaItemDTO{
		NodeID:    nid,
		Name:      n.Name,
		Mime:      n.Mime.String,
		Version:   n.Version,
		StreamURL: "/files/" + nid + "/stream?t=" + tok,
	}
	if n.Size.Valid {
		size := n.Size.Int64
		item.Size = &size
	}
	if db.UUIDString(song.NodeID) != nid {
		return item, nil // no index row for this node
	}
	item.Indexed = true
	item.Title = song.Title
	item.Artist = song.ArtistName.String
	item.Album = song.AlbumName.String
	if song.Track.Valid {
		item.Track = &song.Track.Int32
	}
	if song.Disc.Valid {
		item.Disc = &song.Disc.Int32
	}
	if song.Duration.Valid {
		item.Duration = &song.Duration.Int32
	}
	if song.Bitrate.Valid {
		item.Bitrate = &song.Bitrate.Int32
	}
	return item, nil
}
