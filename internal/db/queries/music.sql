-- name: GetMusicSettings :one
SELECT * FROM music_settings WHERE user_id = $1;

-- name: UpsertMusicSettings :one
INSERT INTO music_settings (user_id, enabled, folder_node_id, tag_edit_versioning, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (user_id) DO UPDATE SET enabled = EXCLUDED.enabled,
    folder_node_id = EXCLUDED.folder_node_id,
    tag_edit_versioning = EXCLUDED.tag_edit_versioning,
    updated_at = now()
RETURNING *;

-- name: SetMusicCredentials :exec
UPDATE music_settings SET password_cipher = $2, api_key = $3, token_version = $4, updated_at = now()
WHERE user_id = $1;

-- name: ClearMusicCredentials :exec
UPDATE music_settings SET password_cipher = NULL, api_key = NULL, updated_at = now()
WHERE user_id = $1;

-- name: GetMusicSettingsByApiKey :one
SELECT s.* FROM music_settings s JOIN users u ON u.id = s.user_id
WHERE s.api_key = $1 AND s.token_version = u.token_version AND NOT u.must_change_password;

-- name: EnabledMusicUsers :many
SELECT user_id, folder_node_id FROM music_settings WHERE enabled = true AND folder_node_id IS NOT NULL;

-- name: UpsertArtist :one
INSERT INTO artists (user_id, name, sort_name)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, name) DO UPDATE SET sort_name = EXCLUDED.sort_name
RETURNING *;

-- name: UpsertAlbum :one
INSERT INTO albums (user_id, artist_id, name, year, genre)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, artist_id, name) DO UPDATE
    SET year = EXCLUDED.year, genre = EXCLUDED.genre
RETURNING *;

-- name: UpsertSong :one
INSERT INTO songs (user_id, album_id, artist_id, node_id, title, track, disc, duration, bitrate, suffix, content_type, size, genre)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (node_id) DO UPDATE SET
    user_id      = EXCLUDED.user_id,
    album_id     = EXCLUDED.album_id,
    artist_id    = EXCLUDED.artist_id,
    title        = EXCLUDED.title,
    track        = EXCLUDED.track,
    disc         = EXCLUDED.disc,
    duration     = EXCLUDED.duration,
    bitrate      = EXCLUDED.bitrate,
    suffix       = EXCLUDED.suffix,
    content_type = EXCLUDED.content_type,
    size         = EXCLUDED.size,
    genre        = EXCLUDED.genre,
    updated_at   = now()
RETURNING *;

-- name: DeleteSongByNode :exec
DELETE FROM songs WHERE node_id = $1;

-- name: GetSongByNode :one
SELECT * FROM songs WHERE node_id = $1;

-- name: RefreshAlbumSongCount :exec
UPDATE albums SET song_count = (SELECT count(*) FROM songs WHERE songs.album_id = albums.id) WHERE albums.id = $1;

-- name: SetAlbumCover :exec
UPDATE albums SET cover_art = $2 WHERE albums.id = $1;

-- name: SetArtistCover :exec
UPDATE artists SET cover_art = $2 WHERE artists.id = $1;

-- name: ListFileNodesUnderFolder :many
WITH RECURSIVE subtree AS (
    SELECT nodes.id FROM nodes WHERE nodes.id = $1
    UNION ALL
    SELECT n.id FROM nodes n JOIN subtree s ON n.parent_id = s.id
)
SELECT n.* FROM nodes n JOIN subtree s ON n.id = s.id
WHERE n.is_dir = false AND n.deleted_at IS NULL;

-- name: AccessibleSongs :many
-- Returns all songs accessible to a user: own songs plus songs whose file node is
-- inside a folder shared to that user (recursively), excluding expired shares.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.* FROM songs s
WHERE s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree)
ORDER BY s.title;

-- name: AccessibleArtists :many
-- Returns all artists that have at least one accessible song for the user.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT a.* FROM artists a
JOIN songs s ON s.artist_id = a.id
WHERE s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree)
ORDER BY a.sort_name, a.name;

-- name: AccessibleArtist :one
-- Returns a single artist if the user has at least one accessible song by that artist.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT a.* FROM artists a
JOIN songs s ON s.artist_id = a.id
WHERE a.id = $2
  AND (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
LIMIT 1;

-- name: AccessibleAlbumsByArtist :many
-- Returns albums for a given artist that contain at least one accessible song.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT al.* FROM albums al
JOIN songs s ON s.album_id = al.id
WHERE al.artist_id = $2
  AND (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
ORDER BY al.year, al.name;

-- name: AccessibleAlbum :one
-- Returns a single album if it contains at least one accessible song for the user.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT al.* FROM albums al
JOIN songs s ON s.album_id = al.id
WHERE al.id = $2
  AND (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
LIMIT 1;

-- name: AccessibleSongsByAlbum :many
-- Returns accessible songs for a given album, ordered by disc then track.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.* FROM songs s
WHERE s.album_id = $2
  AND (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
ORDER BY s.disc, s.track;

-- name: AccessibleSong :one
-- Returns a single song by id if it is accessible to the user.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.* FROM songs s
WHERE s.id = $2
  AND (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
LIMIT 1;

-- name: CountAccessibleAlbumsByArtist :one
-- Returns the count of accessible albums for a given artist.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT COUNT(DISTINCT al.id) FROM albums al
JOIN songs s ON s.album_id = al.id
WHERE al.artist_id = $2
  AND (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree));

-- name: GetAlbumWithArtist :one
-- Returns album plus artist name in one query for songChild assembly.
SELECT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
       al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
       ar.name AS artist_name
FROM albums al
JOIN artists ar ON ar.id = al.artist_id
WHERE al.id = $1;

-- name: GetArtistName :one
SELECT name FROM artists WHERE id = $1;

-- name: AccessibleAlbumListNewest :many
-- Returns accessible albums ordered by creation date descending (newest first).
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
       al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
       ar.name AS artist_name,
       COALESCE((SELECT SUM(s2.duration) FROM songs s2 WHERE s2.album_id = al.id), 0)::bigint AS total_duration
FROM albums al
JOIN artists ar ON ar.id = al.artist_id
JOIN songs s ON s.album_id = al.id
WHERE s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree)
ORDER BY al.created_at DESC
LIMIT $2 OFFSET $3;

-- name: AccessibleAlbumListAlpha :many
-- Returns accessible albums ordered by name ascending (alphabeticalByName).
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
       al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
       ar.name AS artist_name,
       COALESCE((SELECT SUM(s2.duration) FROM songs s2 WHERE s2.album_id = al.id), 0)::bigint AS total_duration
FROM albums al
JOIN artists ar ON ar.id = al.artist_id
JOIN songs s ON s.album_id = al.id
WHERE s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree)
ORDER BY al.name ASC
LIMIT $2 OFFSET $3;

-- name: AccessibleAlbumListByYearAsc :many
-- Returns accessible albums for a year range, ordered by year ascending.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
       al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
       ar.name AS artist_name,
       COALESCE((SELECT SUM(s2.duration) FROM songs s2 WHERE s2.album_id = al.id), 0)::bigint AS total_duration
FROM albums al
JOIN artists ar ON ar.id = al.artist_id
JOIN songs s ON s.album_id = al.id
WHERE (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
  AND al.year >= $4 AND al.year <= $5
ORDER BY al.year ASC
LIMIT $2 OFFSET $3;

-- name: AccessibleAlbumListByYearDesc :many
-- Returns accessible albums for a year range, ordered by year descending.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
       al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
       ar.name AS artist_name,
       COALESCE((SELECT SUM(s2.duration) FROM songs s2 WHERE s2.album_id = al.id), 0)::bigint AS total_duration
FROM albums al
JOIN artists ar ON ar.id = al.artist_id
JOIN songs s ON s.album_id = al.id
WHERE (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
  AND al.year >= $4 AND al.year <= $5
ORDER BY al.year DESC
LIMIT $2 OFFSET $3;

-- name: AccessibleAlbumListByGenre :many
-- Returns accessible albums for a specific genre.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
       al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
       ar.name AS artist_name,
       COALESCE((SELECT SUM(s2.duration) FROM songs s2 WHERE s2.album_id = al.id), 0)::bigint AS total_duration
FROM albums al
JOIN artists ar ON ar.id = al.artist_id
JOIN songs s ON s.album_id = al.id
WHERE (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
  AND al.genre = $4
ORDER BY al.name ASC
LIMIT $2 OFFSET $3;

-- name: AccessibleAlbumListRandom :many
-- Returns accessible albums in random order.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
-- DISTINCT must be applied before the random ordering: Postgres rejects
-- "SELECT DISTINCT ... ORDER BY random()" because random() is not in the select
-- list. Dedupe in a derived table, then order the distinct rows randomly.
SELECT * FROM (
    SELECT DISTINCT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
           al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
           ar.name AS artist_name,
           COALESCE((SELECT SUM(s2.duration) FROM songs s2 WHERE s2.album_id = al.id), 0)::bigint AS total_duration
    FROM albums al
    JOIN artists ar ON ar.id = al.artist_id
    JOIN songs s ON s.album_id = al.id
    WHERE s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree)
) sub
ORDER BY random()
LIMIT $2 OFFSET $3;

-- name: AccessibleAlbumListRecent :many
-- Returns accessible albums ordered by most recent play (from play_history).
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
       al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
       ar.name AS artist_name,
       COALESCE((SELECT SUM(s2.duration) FROM songs s2 WHERE s2.album_id = al.id), 0)::bigint AS total_duration,
       MAX(ph.played_at) AS last_played
FROM albums al
JOIN artists ar ON ar.id = al.artist_id
JOIN songs s ON s.album_id = al.id
LEFT JOIN play_history ph ON ph.song_id = s.id AND ph.user_id = $1
WHERE s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree)
GROUP BY al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
         al.cover_art, al.song_count, al.created_at, al.musicbrainz_id, ar.name
ORDER BY last_played DESC NULLS LAST
LIMIT $2 OFFSET $3;

-- name: AccessibleAlbumListFrequent :many
-- Returns accessible albums ordered by total play count (from play_history).
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
       al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
       ar.name AS artist_name,
       COALESCE((SELECT SUM(s2.duration) FROM songs s2 WHERE s2.album_id = al.id), 0)::bigint AS total_duration,
       COUNT(ph.id) AS play_count
FROM albums al
JOIN artists ar ON ar.id = al.artist_id
JOIN songs s ON s.album_id = al.id
LEFT JOIN play_history ph ON ph.song_id = s.id AND ph.user_id = $1
WHERE s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree)
GROUP BY al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
         al.cover_art, al.song_count, al.created_at, al.musicbrainz_id, ar.name
ORDER BY play_count DESC
LIMIT $2 OFFSET $3;

-- name: AccessibleGenres :many
-- Returns distinct non-empty genres from accessible songs/albums with counts.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT
    g.genre,
    COUNT(DISTINCT s.id) AS song_count,
    COUNT(DISTINCT al.id) AS album_count
FROM (
    SELECT DISTINCT COALESCE(s.genre, al.genre) AS genre
    FROM songs s
    JOIN albums al ON al.id = s.album_id
    WHERE (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
      AND COALESCE(s.genre, al.genre) IS NOT NULL
      AND COALESCE(s.genre, al.genre) != ''
) g
JOIN songs s ON COALESCE(s.genre, (SELECT al2.genre FROM albums al2 WHERE al2.id = s.album_id)) = g.genre
JOIN albums al ON al.id = s.album_id
WHERE s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree)
GROUP BY g.genre
ORDER BY g.genre;

-- name: InsertPlayHistory :exec
INSERT INTO play_history (user_id, song_id) VALUES ($1, $2);

-- name: CreatePlaylist :one
INSERT INTO playlists (user_id, name, comment)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetPlaylist :one
SELECT * FROM playlists WHERE id = $1;

-- name: ListPlaylistsByUser :many
SELECT * FROM playlists WHERE user_id = $1 ORDER BY name;

-- name: UpdatePlaylistMeta :exec
UPDATE playlists SET name = $2, comment = $3, changed_at = now() WHERE id = $1;

-- name: DeletePlaylistForUser :exec
DELETE FROM playlists WHERE id = $1 AND user_id = $2;

-- name: AddPlaylistSong :exec
INSERT INTO playlist_songs (playlist_id, song_id, position) VALUES ($1, $2, $3);

-- name: ClearPlaylistSongs :exec
DELETE FROM playlist_songs WHERE playlist_id = $1;

-- name: GetPlaylistSongs :many
-- Returns songs in a playlist ordered by position, joined with song details.
SELECT s.id, s.user_id, s.album_id, s.artist_id, s.node_id,
       s.title, s.track, s.disc, s.duration, s.bitrate,
       s.suffix, s.content_type, s.size, s.genre, s.musicbrainz_id,
       s.created_at, s.updated_at
FROM playlist_songs ps
JOIN songs s ON s.id = ps.song_id
WHERE ps.playlist_id = $1
ORDER BY ps.position;

-- name: CountPlaylistSongs :one
SELECT COUNT(*) FROM playlist_songs WHERE playlist_id = $1;

-- name: MaxPlaylistPosition :one
SELECT COALESCE(MAX(position), -1) FROM playlist_songs WHERE playlist_id = $1;

-- name: Star :exec
INSERT INTO stars (user_id, item_id, item_type)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: Unstar :exec
DELETE FROM stars WHERE user_id = $1 AND item_id = $2 AND item_type = $3;

-- name: SetRating :exec
INSERT INTO ratings (user_id, item_id, item_type, rating)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, item_id, item_type) DO UPDATE SET rating = EXCLUDED.rating;

-- name: DeleteRating :exec
DELETE FROM ratings WHERE user_id = $1 AND item_id = $2 AND item_type = $3;

-- name: ListStarredSongs :many
-- Returns accessible starred songs for a user, ordered by starred_at desc.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.id, s.user_id, s.album_id, s.artist_id, s.node_id,
       s.title, s.track, s.disc, s.duration, s.bitrate,
       s.suffix, s.content_type, s.size, s.genre, s.musicbrainz_id,
       s.created_at, s.updated_at
FROM stars st
JOIN songs s ON s.id = st.item_id
WHERE st.user_id = $1 AND st.item_type = 'song'
  AND (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
ORDER BY st.starred_at DESC;

-- name: ListStarredAlbums :many
-- Returns accessible starred albums for a user, ordered by starred_at desc.
-- An album is accessible iff it contains at least one song owned by or shared with
-- the caller (same own ∪ shared_subtree filter as ListStarredSongs).
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
-- EXISTS (not JOIN songs) keeps stars→albums 1:1, so no DISTINCT is needed and
-- ORDER BY st.starred_at stays valid (DISTINCT + ORDER BY a non-selected column errors).
SELECT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
       al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
       ar.name AS artist_name
FROM stars st
JOIN albums al ON al.id = st.item_id
JOIN artists ar ON ar.id = al.artist_id
WHERE st.user_id = $1 AND st.item_type = 'album'
  AND EXISTS (
    SELECT 1 FROM songs s
    WHERE s.album_id = al.id
      AND (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
  )
ORDER BY st.starred_at DESC;

-- name: ListStarredArtists :many
-- Returns accessible starred artists for a user, ordered by starred_at desc.
-- An artist is accessible iff it has at least one accessible song (own ∪ shared).
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
-- EXISTS keeps stars→artists 1:1 (no DISTINCT), so ORDER BY st.starred_at is valid.
SELECT a.id, a.user_id, a.name, a.sort_name, a.cover_art, a.musicbrainz_id
FROM stars st
JOIN artists a ON a.id = st.item_id
WHERE st.user_id = $1 AND st.item_type = 'artist'
  AND EXISTS (
    SELECT 1 FROM songs s
    WHERE s.artist_id = a.id
      AND (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
  )
ORDER BY st.starred_at DESC;

-- name: SearchArtists :many
-- Returns accessible artists whose name matches a case-insensitive substring.
-- Pass an empty string for q to match all artists.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = sqlc.arg(user_id)::uuid
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT a.id, a.user_id, a.name, a.sort_name, a.cover_art, a.musicbrainz_id
FROM artists a
JOIN songs s ON s.artist_id = a.id
WHERE (s.user_id = sqlc.arg(user_id) OR s.node_id IN (SELECT node_id FROM shared_subtree))
  AND a.name ILIKE '%' || sqlc.arg(query) || '%'
ORDER BY a.sort_name, a.name
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: SearchAlbums :many
-- Returns accessible albums whose name matches a case-insensitive substring.
-- Pass an empty string for q to match all albums.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = sqlc.arg(user_id)::uuid
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT al.id, al.user_id, al.artist_id, al.name, al.year, al.genre,
       al.cover_art, al.song_count, al.created_at, al.musicbrainz_id,
       ar.name AS artist_name
FROM albums al
JOIN artists ar ON ar.id = al.artist_id
JOIN songs s ON s.album_id = al.id
WHERE (s.user_id = sqlc.arg(user_id) OR s.node_id IN (SELECT node_id FROM shared_subtree))
  AND al.name ILIKE '%' || sqlc.arg(query) || '%'
ORDER BY al.name
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: SearchSongs :many
-- Returns accessible songs whose title matches a case-insensitive substring.
-- Pass an empty string for q to match all songs.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = sqlc.arg(user_id)::uuid
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.id, s.user_id, s.album_id, s.artist_id, s.node_id,
       s.title, s.track, s.disc, s.duration, s.bitrate,
       s.suffix, s.content_type, s.size, s.genre, s.musicbrainz_id,
       s.created_at, s.updated_at,
       al.name AS album_name, ar.name AS artist_name, al.year AS album_year
FROM songs s
JOIN albums al ON al.id = s.album_id
JOIN artists ar ON ar.id = s.artist_id
WHERE (s.user_id = sqlc.arg(user_id) OR s.node_id IN (SELECT node_id FROM shared_subtree))
  AND s.title ILIKE '%' || sqlc.arg(query) || '%'
ORDER BY s.title
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: RandomAccessibleSongs :many
-- Returns accessible songs in random order with optional genre/year filters.
-- Pass NULL for genre, from_year, to_year to skip those filters.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = sqlc.arg(user_id)::uuid
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.id, s.user_id, s.album_id, s.artist_id, s.node_id,
       s.title, s.track, s.disc, s.duration, s.bitrate,
       s.suffix, s.content_type, s.size, s.genre, s.musicbrainz_id,
       s.created_at, s.updated_at,
       al.name AS album_name, ar.name AS artist_name, al.year AS album_year
FROM songs s
JOIN albums al ON al.id = s.album_id
JOIN artists ar ON ar.id = s.artist_id
WHERE (s.user_id = sqlc.arg(user_id) OR s.node_id IN (SELECT node_id FROM shared_subtree))
  AND (sqlc.narg(genre)::text IS NULL OR COALESCE(s.genre, al.genre) = sqlc.narg(genre))
  AND (sqlc.narg(from_year)::int IS NULL OR al.year >= sqlc.narg(from_year))
  AND (sqlc.narg(to_year)::int IS NULL OR al.year <= sqlc.narg(to_year))
ORDER BY random()
LIMIT sqlc.arg(lim);

-- name: SongsByGenre :many
-- Returns accessible songs for a specific genre with pagination.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = sqlc.arg(user_id)::uuid
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.id, s.user_id, s.album_id, s.artist_id, s.node_id,
       s.title, s.track, s.disc, s.duration, s.bitrate,
       s.suffix, s.content_type, s.size, s.genre, s.musicbrainz_id,
       s.created_at, s.updated_at,
       al.name AS album_name, ar.name AS artist_name, al.year AS album_year
FROM songs s
JOIN albums al ON al.id = s.album_id
JOIN artists ar ON ar.id = s.artist_id
WHERE (s.user_id = sqlc.arg(user_id) OR s.node_id IN (SELECT node_id FROM shared_subtree))
  AND COALESCE(s.genre, al.genre) = sqlc.arg(genre)
ORDER BY al.name, s.disc, s.track
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: StarredItems :many
SELECT item_id, item_type, starred_at FROM stars WHERE user_id = $1;

-- name: RatingsForUser :many
SELECT item_id, item_type, rating FROM ratings WHERE user_id = $1;

-- name: RecentPlayHistory :many
-- Returns recent play_history rows for the caller, joined to song+album+artist.
SELECT s.id, s.user_id, s.album_id, s.artist_id, s.node_id,
       s.title, s.track, s.disc, s.duration, s.bitrate,
       s.suffix, s.content_type, s.size, s.genre, s.musicbrainz_id,
       s.created_at, s.updated_at,
       al.name AS album_name, ar.name AS artist_name, al.year AS album_year,
       ph.played_at
FROM play_history ph
JOIN songs s ON s.id = ph.song_id
JOIN albums al ON al.id = s.album_id
JOIN artists ar ON ar.id = s.artist_id
WHERE ph.user_id = sqlc.arg(user_id)::uuid
ORDER BY ph.played_at DESC
LIMIT sqlc.arg(lim);

-- name: AccessibleSongByTitle :one
-- Returns one accessible song matching the given title (case-insensitive),
-- for the legacy getLyrics endpoint which is keyed by artist+title.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.* FROM songs s
WHERE lower(s.title) = lower($2)
  AND (s.user_id = $1 OR s.node_id IN (SELECT node_id FROM shared_subtree))
LIMIT 1;

-- name: UpsertPlayQueue :exec
INSERT INTO play_queues (user_id, current_id, position_ms, changed_by, changed_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (user_id) DO UPDATE
SET current_id = EXCLUDED.current_id,
    position_ms = EXCLUDED.position_ms,
    changed_by = EXCLUDED.changed_by,
    changed_at = now();

-- name: ClearPlayQueueEntries :exec
DELETE FROM play_queue_entries WHERE user_id = $1;

-- name: AddPlayQueueEntry :exec
INSERT INTO play_queue_entries (user_id, idx, song_id) VALUES ($1, $2, $3);

-- name: GetPlayQueue :one
SELECT user_id, current_id, position_ms, changed_by, changed_at
FROM play_queues WHERE user_id = $1;

-- name: GetPlayQueueEntries :many
SELECT s.*,
       al.name AS album_name, ar.name AS artist_name, al.year AS album_year
FROM play_queue_entries e
JOIN songs s ON s.id = e.song_id
JOIN albums al ON al.id = s.album_id
JOIN artists ar ON ar.id = s.artist_id
WHERE e.user_id = $1
ORDER BY e.idx;

-- name: AccessibleSongsByArtist :many
-- Returns accessible songs for a given artist ordered by title, with album/artist names.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = sqlc.arg(user_id)::uuid
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.*, al.name AS album_name, ar.name AS artist_name, al.year AS album_year
FROM songs s
JOIN albums al ON al.id = s.album_id
JOIN artists ar ON ar.id = s.artist_id
WHERE s.artist_id = sqlc.arg(artist_id)::uuid
  AND (s.user_id = sqlc.arg(user_id) OR s.node_id IN (SELECT node_id FROM shared_subtree))
ORDER BY s.title
LIMIT sqlc.arg(lim);

-- name: TopSongsByArtistName :many
-- Returns accessible songs for a given artist name ordered by caller's play count descending.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = sqlc.arg(user_id)::uuid
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.*, al.name AS album_name, ar.name AS artist_name, al.year AS album_year,
       COUNT(ph.id) AS play_count
FROM songs s
JOIN albums al ON al.id = s.album_id
JOIN artists ar ON ar.id = s.artist_id
LEFT JOIN play_history ph ON ph.song_id = s.id AND ph.user_id = sqlc.arg(user_id)
WHERE lower(ar.name) = lower(sqlc.arg(artist_name))
  AND (s.user_id = sqlc.arg(user_id) OR s.node_id IN (SELECT node_id FROM shared_subtree))
GROUP BY s.id, al.name, ar.name, al.year
ORDER BY play_count DESC, s.title
LIMIT sqlc.arg(lim);

-- name: SimilarSongsByGenre :many
-- Returns accessible songs of a given genre excluding a specific artist, in random order.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = sqlc.arg(user_id)::uuid
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT s.*, al.name AS album_name, ar.name AS artist_name, al.year AS album_year
FROM songs s
JOIN albums al ON al.id = s.album_id
JOIN artists ar ON ar.id = s.artist_id
WHERE s.genre = sqlc.arg(genre)
  AND s.artist_id <> sqlc.arg(exclude_artist_id)::uuid
  AND (s.user_id = sqlc.arg(user_id) OR s.node_id IN (SELECT node_id FROM shared_subtree))
ORDER BY random()
LIMIT sqlc.arg(lim);

-- name: ListInternetRadioStations :many
SELECT id, user_id, name, stream_url, homepage_url, created_at
FROM internet_radio_stations
WHERE user_id = $1
ORDER BY name;

-- name: CreateInternetRadioStation :one
INSERT INTO internet_radio_stations (user_id, name, stream_url, homepage_url)
VALUES ($1, $2, $3, $4)
RETURNING id, user_id, name, stream_url, homepage_url, created_at;

-- name: UpdateInternetRadioStation :execrows
UPDATE internet_radio_stations
SET name = $3, stream_url = $4, homepage_url = $5
WHERE id = $1 AND user_id = $2;

-- name: DeleteInternetRadioStation :execrows
DELETE FROM internet_radio_stations
WHERE id = $1 AND user_id = $2;

-- name: CreatePodcastChannel :one
INSERT INTO podcast_channels (user_id, feed_url, title, description, cover_url)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, feed_url) DO UPDATE SET feed_url = EXCLUDED.feed_url
RETURNING *;

-- name: GetPodcastChannelForUser :one
SELECT * FROM podcast_channels WHERE id = $1 AND user_id = $2;

-- name: GetPodcastChannelByFeed :one
SELECT * FROM podcast_channels WHERE user_id = $1 AND feed_url = $2;

-- name: ListPodcastChannelsForUser :many
SELECT * FROM podcast_channels WHERE user_id = $1 ORDER BY title;

-- name: ListAllPodcastChannels :many
SELECT * FROM podcast_channels;

-- name: SetPodcastChannelMeta :exec
UPDATE podcast_channels
SET title = $3, description = $4, cover_url = $5, last_fetched_at = now()
WHERE id = $1 AND user_id = $2;

-- name: SetPodcastChannelCoverPath :exec
UPDATE podcast_channels SET cover_path = $2 WHERE id = $1 AND user_id = $3;

-- name: DeletePodcastChannelForUser :execrows
DELETE FROM podcast_channels WHERE id = $1 AND user_id = $2;

-- name: UpsertPodcastEpisode :exec
INSERT INTO podcast_episodes
    (channel_id, user_id, title, description, pub_date, audio_url, duration, suffix, content_type)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (channel_id, audio_url) DO NOTHING;

-- name: ListEpisodesByChannel :many
SELECT * FROM podcast_episodes WHERE channel_id = $1 ORDER BY pub_date DESC NULLS LAST, created_at DESC;

-- name: ListNewestEpisodesForUser :many
SELECT * FROM podcast_episodes WHERE user_id = $1
ORDER BY pub_date DESC NULLS LAST, created_at DESC
LIMIT $2;

-- name: GetEpisodeForUser :one
SELECT * FROM podcast_episodes WHERE id = $1 AND user_id = $2;

-- name: SetEpisodeStatus :exec
UPDATE podcast_episodes SET status = $2 WHERE id = $1 AND user_id = $3;

-- name: ClaimEpisodeForDownload :execrows
UPDATE podcast_episodes SET status = 'downloading'
WHERE id = $1 AND user_id = $2 AND status NOT IN ('downloading','completed');

-- name: SetEpisodeDownloaded :exec
UPDATE podcast_episodes
SET status = 'completed', disk_path = $2, size = $3, content_type = $4, suffix = $5
WHERE id = $1 AND user_id = $6;

-- name: ClearEpisodeDownload :exec
UPDATE podcast_episodes SET status = 'skipped', disk_path = NULL WHERE id = $1 AND user_id = $2;

-- name: DeletePodcastEpisodeForUser :execrows
DELETE FROM podcast_episodes WHERE id = $1 AND user_id = $2;

-- name: ListCompletedEpisodesByChannelDesc :many
SELECT id, disk_path FROM podcast_episodes
WHERE channel_id = $1 AND status = 'completed'
ORDER BY pub_date DESC NULLS LAST, created_at DESC;

-- name: CreateBookmark :exec
INSERT INTO bookmarks (user_id, item_id, item_type, position_ms, comment, changed_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (user_id, item_id, item_type) DO UPDATE
SET position_ms = EXCLUDED.position_ms, comment = EXCLUDED.comment, changed_at = now();

-- name: ListBookmarks :many
SELECT * FROM bookmarks WHERE user_id = $1 ORDER BY changed_at DESC;

-- name: DeleteBookmark :exec
DELETE FROM bookmarks WHERE user_id = $1 AND item_id = $2 AND item_type = $3;

-- Song metadata for the player's media listing: one query covers a whole folder,
-- names are joined in (songs stores only artist_id/album_id).
-- name: SongsMetaByNodeIDs :many
SELECT s.node_id, s.title, s.track, s.disc, s.duration, s.bitrate,
       a.name AS artist_name, al.name AS album_name
FROM songs s
LEFT JOIN artists a ON a.id = s.artist_id
LEFT JOIN albums al ON al.id = s.album_id
WHERE s.node_id = ANY($1::uuid[]);
