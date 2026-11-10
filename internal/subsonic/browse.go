package subsonic

import (
	"context"
	"sort"
	"unicode"
	"unicode/utf8"

	"discodrive/internal/db"

	"github.com/jackc/pgx/v5/pgtype"
)

func init() {
	endpoints["getMusicFolders"] = getMusicFolders
	endpoints["getArtists"] = getArtists
	endpoints["getIndexes"] = getIndexes
	endpoints["getArtist"] = getArtist
	endpoints["getAlbum"] = getAlbum
	endpoints["getSong"] = getSong
}

// getMusicFolders returns a single synthetic "Music" folder (v1 approach).
func getMusicFolders(h *Handler, c *reqCtx) {
	c.ok(map[string]any{
		"musicFolders": map[string]any{
			"musicFolder": []map[string]any{
				{"id": 0, "name": "Music"},
			},
		},
	})
}

// artistIndex is one letter-group in the Subsonic index response.
type artistIndex struct {
	Name   string           `json:"name"`
	Artist []map[string]any `json:"artist"`
}

// buildArtistIndex fetches all accessible artists for the user and groups them
// by the first letter of their sort_name (falling back to name). It returns the
// slice of index groups sorted by letter, plus any error.
func buildArtistIndex(ctx context.Context, h *Handler, userID pgtype.UUID, marks userMarks) ([]map[string]any, error) {
	artists, err := h.q.AccessibleArtists(ctx, userID)
	if err != nil {
		return nil, err
	}

	albums := h.albumCounts(ctx, userID)

	// group → []artistObj
	groups := map[string][]map[string]any{}
	for _, a := range artists {
		sortKey := a.SortName
		if sortKey == "" {
			sortKey = a.Name
		}
		letter := indexLetter(sortKey)

		albumCount := albums[a.ID]

		aUUID := db.UUIDString(a.ID)
		obj := map[string]any{
			"id":         encID("ar", aUUID),
			"name":       a.Name,
			"albumCount": albumCount,
			"coverArt":   encID("ar", aUUID),
		}
		if a.MusicbrainzID.Valid {
			obj["musicBrainzId"] = a.MusicbrainzID.String
		}
		if s := marks.starredAt("artist", aUUID); s != "" {
			obj["starred"] = s
		}
		if r := marks.ratingOf("artist", aUUID); r > 0 {
			obj["userRating"] = r
		}
		groups[letter] = append(groups[letter], obj)
	}

	// Sort letters.
	letters := make([]string, 0, len(groups))
	for l := range groups {
		letters = append(letters, l)
	}
	sort.Strings(letters)

	index := make([]map[string]any, 0, len(letters))
	for _, l := range letters {
		index = append(index, map[string]any{
			"name":   l,
			"artist": groups[l],
		})
	}
	return index, nil
}

// indexLetter returns the uppercase first character to use as the index letter.
// Non-letter runes (numbers, symbols) all bucket into "#".
func indexLetter(s string) string {
	if s == "" {
		return "#"
	}
	r, _ := utf8.DecodeRuneInString(s)
	r = unicode.ToUpper(r)
	if unicode.IsLetter(r) {
		return string(r)
	}
	return "#"
}

// getArtists implements the getArtists Subsonic endpoint.
func getArtists(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	marks := h.loadMarks(ctx, c.userID)
	index, err := buildArtistIndex(ctx, h, userUUID, marks)
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	c.ok(map[string]any{
		"artists": map[string]any{
			"ignoredArticles": "",
			"index":           index,
		},
	})
}

// getIndexes is an alias for getArtists with a different wrapper key (legacy Subsonic clients).
func getIndexes(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	marks := h.loadMarks(ctx, c.userID)
	index, err := buildArtistIndex(ctx, h, userUUID, marks)
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	c.ok(map[string]any{
		"indexes": map[string]any{
			"ignoredArticles": "",
			"index":           index,
		},
	})
}

// getArtist returns a single artist and its albums.
func getArtist(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	kind, uuid, ok := decID(c.param("id"))
	if !ok || kind != "ar" {
		c.fail(ErrNotFound, "artist not found")
		return
	}
	artistUUID, err := db.ParseUUID(uuid)
	if err != nil {
		c.fail(ErrNotFound, "artist not found")
		return
	}

	artist, err := h.q.AccessibleArtist(ctx, db.AccessibleArtistParams{
		UserID: userUUID,
		ID:     artistUUID,
	})
	if err != nil {
		c.fail(ErrNotFound, "artist not found")
		return
	}

	albums, err := h.q.AccessibleAlbumsByArtist(ctx, db.AccessibleAlbumsByArtistParams{
		UserID:   userUUID,
		ArtistID: artistUUID,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	albumCount, _ := h.q.CountAccessibleAlbumsByArtist(ctx, db.CountAccessibleAlbumsByArtistParams{
		UserID:   userUUID,
		ArtistID: artistUUID,
	})

	marks := h.loadMarks(ctx, c.userID)
	artistUUIDStr := db.UUIDString(artist.ID)
	artistObj := map[string]any{
		"id":         encID("ar", artistUUIDStr),
		"name":       artist.Name,
		"albumCount": albumCount,
		"coverArt":   encID("ar", artistUUIDStr),
		"album":      buildAlbumList(albums, artist.Name, marks),
	}
	if artist.MusicbrainzID.Valid {
		artistObj["musicBrainzId"] = artist.MusicbrainzID.String
	}
	if s := marks.starredAt("artist", artistUUIDStr); s != "" {
		artistObj["starred"] = s
	}
	if r := marks.ratingOf("artist", artistUUIDStr); r > 0 {
		artistObj["userRating"] = r
	}

	c.ok(map[string]any{"artist": artistObj})
}

// getAlbum returns a single album with its songs.
func getAlbum(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	kind, uuid, ok := decID(c.param("id"))
	if !ok || kind != "al" {
		c.fail(ErrNotFound, "album not found")
		return
	}
	albumUUID, err := db.ParseUUID(uuid)
	if err != nil {
		c.fail(ErrNotFound, "album not found")
		return
	}

	album, err := h.q.AccessibleAlbum(ctx, db.AccessibleAlbumParams{
		UserID: userUUID,
		ID:     albumUUID,
	})
	if err != nil {
		c.fail(ErrNotFound, "album not found")
		return
	}

	// Fetch artist name for the album object.
	artistName, _ := h.q.GetArtistName(ctx, album.ArtistID)

	songs, err := h.q.AccessibleSongsByAlbum(ctx, db.AccessibleSongsByAlbumParams{
		UserID:  userUUID,
		AlbumID: albumUUID,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	// Compute total duration from songs.
	var totalDuration int32
	for _, s := range songs {
		if s.Duration.Valid {
			totalDuration += s.Duration.Int32
		}
	}

	marks := h.loadMarks(ctx, c.userID)
	albumUUIDStr := db.UUIDString(album.ID)

	songChildren := make([]map[string]any, 0, len(songs))
	for _, s := range songs {
		sUUID := db.UUIDString(s.ID)
		songChildren = append(songChildren, buildSongChild(s, album.Name, artistName, album.Year,
			marks.starredAt("song", sUUID), marks.ratingOf("song", sUUID)))
	}

	albumObj := map[string]any{
		"id":        encID("al", albumUUIDStr),
		"name":      album.Name,
		"artist":    artistName,
		"artistId":  encID("ar", db.UUIDString(album.ArtistID)),
		"coverArt":  encID("al", albumUUIDStr),
		"songCount": album.SongCount,
		"duration":  totalDuration,
		"song":      songChildren,
	}
	if album.Year.Valid {
		albumObj["year"] = album.Year.Int32
	}
	if album.Genre.Valid {
		albumObj["genre"] = album.Genre.String
	}
	if album.MusicbrainzID.Valid {
		albumObj["musicBrainzId"] = album.MusicbrainzID.String
	}
	if s := marks.starredAt("album", albumUUIDStr); s != "" {
		albumObj["starred"] = s
	}
	if r := marks.ratingOf("album", albumUUIDStr); r > 0 {
		albumObj["userRating"] = r
	}

	c.ok(map[string]any{"album": albumObj})
}

// getSong returns a single song by id.
func getSong(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	kind, uuid, ok := decID(c.param("id"))
	if !ok || kind != "tr" {
		c.fail(ErrNotFound, "song not found")
		return
	}
	songUUID, err := db.ParseUUID(uuid)
	if err != nil {
		c.fail(ErrNotFound, "song not found")
		return
	}

	song, err := h.q.AccessibleSong(ctx, db.AccessibleSongParams{
		UserID: userUUID,
		ID:     songUUID,
	})
	if err != nil {
		c.fail(ErrNotFound, "song not found")
		return
	}

	// Fetch album and artist names.
	albumRow, err := h.q.GetAlbumWithArtist(ctx, song.AlbumID)
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	marks := h.loadMarks(ctx, c.userID)
	sUUID := db.UUIDString(song.ID)
	c.ok(map[string]any{
		"song": buildSongChild(song, albumRow.Name, albumRow.ArtistName, albumRow.Year,
			marks.starredAt("song", sUUID), marks.ratingOf("song", sUUID)),
	})
}

// buildSongChild constructs the canonical Subsonic Child object for a track.
// albumYear is passed in rather than fetched again to avoid extra queries.
// starred is an RFC3339 string (empty means not starred); userRating is 1–5 (0 means none).
func buildSongChild(s db.Song, albumName, artistName string, albumYear pgtype.Int4, starred string, userRating int) map[string]any {
	obj := map[string]any{
		"id":       encID("tr", db.UUIDString(s.ID)),
		"parent":   encID("al", db.UUIDString(s.AlbumID)),
		"isDir":    false,
		"title":    s.Title,
		"album":    albumName,
		"artist":   artistName,
		"coverArt": encID("al", db.UUIDString(s.AlbumID)),
		"albumId":  encID("al", db.UUIDString(s.AlbumID)),
		"artistId": encID("ar", db.UUIDString(s.ArtistID)),
		"type":     "music",
	}
	if s.Track.Valid {
		obj["track"] = s.Track.Int32
	}
	if albumYear.Valid {
		obj["year"] = albumYear.Int32
	}
	if s.Genre.Valid {
		obj["genre"] = s.Genre.String
	}
	if s.Size.Valid {
		obj["size"] = s.Size.Int64
	}
	if s.ContentType.Valid {
		obj["contentType"] = s.ContentType.String
	}
	if s.Suffix.Valid {
		obj["suffix"] = s.Suffix.String
	}
	if s.Duration.Valid {
		obj["duration"] = s.Duration.Int32
	}
	if s.Bitrate.Valid {
		obj["bitRate"] = s.Bitrate.Int32
	}
	if s.MusicbrainzID.Valid {
		obj["musicBrainzId"] = s.MusicbrainzID.String
	}
	if s.Disc.Valid {
		obj["discNumber"] = s.Disc.Int32
	}
	if starred != "" {
		obj["starred"] = starred
	}
	if userRating > 0 {
		obj["userRating"] = userRating
	}
	return obj
}

// buildAlbumList converts a slice of db.Album into Subsonic album objects.
// marks is used to populate starred/userRating for each album.
func buildAlbumList(albums []db.Album, artistName string, marks userMarks) []map[string]any {
	result := make([]map[string]any, 0, len(albums))
	for _, al := range albums {
		uuid := db.UUIDString(al.ID)
		obj := map[string]any{
			"id":        encID("al", uuid),
			"name":      al.Name,
			"artist":    artistName,
			"artistId":  encID("ar", db.UUIDString(al.ArtistID)),
			"coverArt":  encID("al", uuid),
			"songCount": al.SongCount,
		}
		if al.Year.Valid {
			obj["year"] = al.Year.Int32
		}
		if al.Genre.Valid {
			obj["genre"] = al.Genre.String
		}
		if al.MusicbrainzID.Valid {
			obj["musicBrainzId"] = al.MusicbrainzID.String
		}
		if s := marks.starredAt("album", uuid); s != "" {
			obj["starred"] = s
		}
		if r := marks.ratingOf("album", uuid); r > 0 {
			obj["userRating"] = r
		}
		result = append(result, obj)
	}
	return result
}

// albumCounts returns the number of accessible albums per artist in one query. A failed
// read gives an empty map: the counts show as 0, as a failed per-artist count did.
func (h *Handler) albumCounts(ctx context.Context, userID pgtype.UUID) map[pgtype.UUID]int64 {
	rows, _ := h.q.CountAccessibleAlbumsPerArtist(ctx, userID)
	out := make(map[pgtype.UUID]int64, len(rows))
	for _, r := range rows {
		out[r.ArtistID] = r.Albums
	}
	return out
}

// albumsOf returns the albums (with artist names) of a list of songs in one query. A
// failed read gives an empty map: the songs then show without album and artist, as a
// failed per-song lookup did.
func (h *Handler) albumsOf(ctx context.Context, songs []db.Song) map[pgtype.UUID]db.AlbumsWithArtistRow {
	seen := make(map[pgtype.UUID]bool, len(songs))
	ids := make([]pgtype.UUID, 0, len(songs))
	for _, s := range songs {
		if !seen[s.AlbumID] {
			seen[s.AlbumID] = true
			ids = append(ids, s.AlbumID)
		}
	}
	rows, _ := h.q.AlbumsWithArtist(ctx, ids)
	out := make(map[pgtype.UUID]db.AlbumsWithArtistRow, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}
