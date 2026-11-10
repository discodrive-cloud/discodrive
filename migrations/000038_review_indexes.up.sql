-- Indexes the 2026-11 review found missing. Plain CREATE INDEX (not CONCURRENTLY): the
-- server applies migrations before it starts serving, so there are no writers to keep
-- unblocked, and one file can hold them all.

-- nodes.parent_id (ON DELETE CASCADE) and nodes.conflict_of (ON DELETE SET NULL) reference
-- nodes itself. The foreign-key triggers look rows up by that column alone, and every
-- existing index on parent_id is partial or leads with user_id, so each hard-deleted node
-- scanned the whole table twice. The same index serves listings and the recursive
-- shared-subtree queries, which walk children by parent_id without user_id.
CREATE INDEX IF NOT EXISTS nodes_parent ON nodes (parent_id);
CREATE INDEX IF NOT EXISTS nodes_conflict_of ON nodes (conflict_of) WHERE conflict_of IS NOT NULL;

-- Album lists sum durations per album and the scanner recounts an album per indexed song,
-- both by album_id alone; songs_user_album leads with user_id.
CREATE INDEX IF NOT EXISTS songs_album ON songs (album_id);
CREATE INDEX IF NOT EXISTS songs_artist ON songs (artist_id);

-- Cascades from songs(id): deleting a song scanned each of these tables.
CREATE INDEX IF NOT EXISTS play_history_song ON play_history (song_id);
CREATE INDEX IF NOT EXISTS playlist_songs_song ON playlist_songs (song_id);
CREATE INDEX IF NOT EXISTS play_queue_entries_song ON play_queue_entries (song_id);

-- The transcode worker polls this queue every two seconds; it is almost always empty, and
-- without the index each poll read every photo_assets row.
CREATE INDEX IF NOT EXISTS photo_assets_transcode_pending ON photo_assets (id)
    WHERE kind = 'video' AND transcode_state = 'pending';
