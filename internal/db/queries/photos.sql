-- name: GetPhotoSettings :one
SELECT * FROM photo_settings WHERE user_id = $1;

-- name: UpsertPhotoSettings :one
INSERT INTO photo_settings (user_id, enabled) VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()
RETURNING *;

-- The cursor only moves forward: a catch-up that started before a source change must
-- not undo the cursor the change set.
-- name: SetPhotoIndexedSeq :exec
UPDATE photo_settings SET indexed_seq = GREATEST(indexed_seq, $2) WHERE user_id = $1;

-- name: ListPhotoSources :many
SELECT * FROM photo_sources WHERE user_id = $1 ORDER BY created_at, id;

-- name: DeletePhotoSources :exec
DELETE FROM photo_sources WHERE user_id = $1;

-- name: InsertPhotoSource :one
INSERT INTO photo_sources (user_id, folder_node_id) VALUES ($1, $2)
ON CONFLICT (user_id, folder_node_id) DO UPDATE SET folder_node_id = EXCLUDED.folder_node_id
RETURNING *;

-- The indexer owns the metadata columns; the user's own state (favorite, hidden, trashed_at,
-- parent_id, role) survives a re-index. The thumbnail goes back to pending when the file
-- changed (node_version differs).
-- name: UpsertPhotoAsset :one
INSERT INTO photo_assets (
    user_id, node_id, kind, taken_at, taken_local, tz_offset_min, date_source,
    lat, lon, place_id, country_code, make, model, lens, f_number, focal_mm, exposure, iso,
    width, height, duration_ms, video_codec, is_hdr, live_cid, content_hash,
    indexed_at, node_version, index_error
) VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
    $19, $20, $21, $22, $23, $24, $25,
    now(), $26, $27
)
ON CONFLICT (node_id) DO UPDATE SET
    kind = EXCLUDED.kind,
    taken_at = EXCLUDED.taken_at,
    taken_local = EXCLUDED.taken_local,
    tz_offset_min = EXCLUDED.tz_offset_min,
    -- An edited date stays "edit" while a re-read of the (edited) file reports the same instant.
    date_source = CASE WHEN photo_assets.date_source = 'edit' AND EXCLUDED.date_source IN ('exif', 'xmp')
                        AND EXCLUDED.taken_at = photo_assets.taken_at
                       THEN 'edit' ELSE EXCLUDED.date_source END,
    lat = CASE WHEN photo_assets.place_cleared AND EXCLUDED.content_hash IS NOT DISTINCT FROM photo_assets.content_hash
               THEN NULL ELSE EXCLUDED.lat END,
    lon = CASE WHEN photo_assets.place_cleared AND EXCLUDED.content_hash IS NOT DISTINCT FROM photo_assets.content_hash
               THEN NULL ELSE EXCLUDED.lon END,
    place_id = CASE WHEN photo_assets.place_cleared AND EXCLUDED.content_hash IS NOT DISTINCT FROM photo_assets.content_hash
                 THEN NULL
                 WHEN EXCLUDED.lat IS NOT DISTINCT FROM photo_assets.lat
                  AND EXCLUDED.lon IS NOT DISTINCT FROM photo_assets.lon
                 THEN COALESCE(EXCLUDED.place_id, photo_assets.place_id) ELSE EXCLUDED.place_id END,
    country_code = CASE WHEN photo_assets.place_cleared AND EXCLUDED.content_hash IS NOT DISTINCT FROM photo_assets.content_hash
                   THEN NULL
                   WHEN EXCLUDED.lat IS NOT DISTINCT FROM photo_assets.lat
                    AND EXCLUDED.lon IS NOT DISTINCT FROM photo_assets.lon
                   THEN COALESCE(EXCLUDED.country_code, photo_assets.country_code) ELSE EXCLUDED.country_code END,
    place_cleared = photo_assets.place_cleared AND EXCLUDED.content_hash IS NOT DISTINCT FROM photo_assets.content_hash,
    make = EXCLUDED.make,
    model = EXCLUDED.model,
    lens = EXCLUDED.lens,
    f_number = EXCLUDED.f_number,
    focal_mm = EXCLUDED.focal_mm,
    exposure = EXCLUDED.exposure,
    iso = EXCLUDED.iso,
    width = EXCLUDED.width,
    height = EXCLUDED.height,
    duration_ms = EXCLUDED.duration_ms,
    video_codec = EXCLUDED.video_codec,
    is_hdr = EXCLUDED.is_hdr,
    live_cid = EXCLUDED.live_cid,
    content_hash = EXCLUDED.content_hash,
    thumb_state = CASE WHEN photo_assets.node_version IS DISTINCT FROM EXCLUDED.node_version
                       THEN 'pending' ELSE photo_assets.thumb_state END,
    thumb_error = CASE WHEN photo_assets.node_version IS DISTINCT FROM EXCLUDED.node_version
                       THEN NULL ELSE photo_assets.thumb_error END,
    transcode_state = CASE WHEN photo_assets.node_version IS DISTINCT FROM EXCLUDED.node_version
                           THEN NULL ELSE photo_assets.transcode_state END,
    indexed_at = now(),
    node_version = EXCLUDED.node_version,
    index_error = EXCLUDED.index_error
RETURNING *;

-- name: GetPhotoAsset :one
SELECT * FROM photo_assets WHERE id = $1 AND user_id = $2;

-- name: GetPhotoAssetByNode :one
SELECT * FROM photo_assets WHERE node_id = $1;

-- name: DeletePhotoAssetByNode :many
DELETE FROM photo_assets WHERE node_id = $1 RETURNING id;

-- Assets of every node under a folder path (the folder was trashed or left the sources;
-- the change log records only the folder itself). Driven from the user's assets, not
-- their nodes.
-- name: DeletePhotoAssetsUnderPath :many
DELETE FROM photo_assets a USING nodes n
WHERE a.user_id = sqlc.arg(user_id) AND n.id = a.node_id
  AND n.disk_path LIKE sqlc.arg(prefix)::text || '/%'
RETURNING a.id;

-- name: TrashPhotoAssetByNode :exec
UPDATE photo_assets SET trashed_at = now() WHERE node_id = $1 AND trashed_at IS NULL;

-- name: UntrashPhotoAssetByNode :exec
UPDATE photo_assets SET trashed_at = NULL WHERE node_id = $1;

-- Media files of the folder's subtree whose asset row is missing or older than the file.
-- The extensions are exactly photos.Indexer.Accepts: a file the change log would not
-- index must not be indexed by a full pass either (the next change would remove it).
-- name: ListStalePhotoNodes :many
WITH RECURSIVE subtree AS (
    SELECT nodes.id FROM nodes WHERE nodes.id = $1
    UNION ALL
    SELECT n.id FROM nodes n JOIN subtree s ON n.parent_id = s.id WHERE n.deleted_at IS NULL
)
SELECT n.* FROM nodes n JOIN subtree s ON n.id = s.id
LEFT JOIN photo_assets a ON a.node_id = n.id
WHERE NOT n.is_dir AND n.deleted_at IS NULL AND NOT n.is_vault AND NOT n.is_conflict_loser
  AND n.name ~* '\.(jpe?g|png|gif|webp|heic|heif|dng|nef|cr2|arw|mov|mp4|m4v|3gp|avi)$'
  AND (a.id IS NULL OR a.node_version IS DISTINCT FROM n.version)
-- Newest first: on the first run the recent photos show up before the old archive.
ORDER BY n.modified_at DESC, n.id;

-- Feed filters shared by ListPhotoMonthBuckets and ListPhotoAssetsInMonth. Hidden, trashed
-- and child rows (live videos, originals) never show. Duplicates (same content_hash within
-- the user) collapse to the row with the smallest id AMONG THE ROWS THAT PASS THE SAME
-- FILTERS, so a copy is not lost because its twin is filtered out; NULL hashes never collapse.
-- The area filter (the map) keeps the cards whose photo_map_key at area_z is in area_keys.
-- Buckets dedup with one window pass over the filtered rows instead of a per-row twin
-- lookup (same result as the month query's NOT EXISTS, several times faster on a big library).
-- name: ListPhotoMonthBuckets :many
WITH f AS (
    SELECT a.id, a.taken_local, a.content_hash
    FROM photo_assets a
    JOIN nodes n ON n.id = a.node_id
    WHERE a.user_id = sqlc.arg(user_id)
  AND a.parent_id IS NULL AND NOT a.hidden AND a.trashed_at IS NULL
  AND (sqlc.narg(kind)::text IS NULL OR a.kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(live)::bool IS NULL OR sqlc.narg(live)::bool = EXISTS (
        SELECT 1 FROM photo_assets c WHERE c.parent_id = a.id AND c.role = 'live-video'))
  AND (sqlc.narg(raw)::bool IS NULL OR sqlc.narg(raw)::bool = (n.name ~* '\.(dng|nef|cr2|arw)$'))
  AND (sqlc.narg(favorite)::bool IS NULL OR a.favorite = sqlc.narg(favorite)::bool)
  AND (sqlc.narg(source_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM photo_sources ps JOIN nodes fn ON fn.id = ps.folder_node_id
        WHERE ps.id = sqlc.narg(source_id)::uuid AND ps.user_id = a.user_id
          AND starts_with(n.disk_path, fn.disk_path || '/')))
  AND (sqlc.narg(area_z)::int IS NULL OR (a.lat IS NOT NULL AND a.lon IS NOT NULL
        AND photo_map_key(a.lat, a.lon, sqlc.narg(area_z)::int) = ANY(sqlc.narg(area_keys)::bigint[])))
), ranked AS (
    SELECT taken_local, content_hash,
           row_number() OVER (PARTITION BY content_hash ORDER BY id) AS rn
    FROM f
)
SELECT date_trunc('month', taken_local)::date AS month, count(*)::bigint AS count
FROM ranked
WHERE content_hash IS NULL OR rn = 1
GROUP BY 1
ORDER BY 1 DESC;

-- name: ListPhotoAssetsInMonth :many
SELECT a.* FROM photo_assets a
JOIN nodes n ON n.id = a.node_id
WHERE a.user_id = sqlc.arg(user_id)
  AND a.taken_local >= sqlc.arg(month)::timestamp
  AND a.taken_local < sqlc.arg(month)::timestamp + interval '1 month'
  AND a.parent_id IS NULL AND NOT a.hidden AND a.trashed_at IS NULL
  AND (sqlc.narg(kind)::text IS NULL OR a.kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(live)::bool IS NULL OR sqlc.narg(live)::bool = EXISTS (
        SELECT 1 FROM photo_assets c WHERE c.parent_id = a.id AND c.role = 'live-video'))
  AND (sqlc.narg(raw)::bool IS NULL OR sqlc.narg(raw)::bool = (n.name ~* '\.(dng|nef|cr2|arw)$'))
  AND (sqlc.narg(favorite)::bool IS NULL OR a.favorite = sqlc.narg(favorite)::bool)
  AND (sqlc.narg(source_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM photo_sources ps JOIN nodes fn ON fn.id = ps.folder_node_id
        WHERE ps.id = sqlc.narg(source_id)::uuid AND ps.user_id = a.user_id
          AND starts_with(n.disk_path, fn.disk_path || '/')))
  AND (sqlc.narg(area_z)::int IS NULL OR (a.lat IS NOT NULL AND a.lon IS NOT NULL
        AND photo_map_key(a.lat, a.lon, sqlc.narg(area_z)::int) = ANY(sqlc.narg(area_keys)::bigint[])))
  AND (a.content_hash IS NULL OR NOT EXISTS (
        SELECT 1 FROM photo_assets d JOIN nodes dn ON dn.id = d.node_id
        WHERE d.user_id = a.user_id AND d.content_hash = a.content_hash AND d.id < a.id
  AND d.parent_id IS NULL AND NOT d.hidden AND d.trashed_at IS NULL
  AND (sqlc.narg(kind)::text IS NULL OR d.kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(live)::bool IS NULL OR sqlc.narg(live)::bool = EXISTS (
        SELECT 1 FROM photo_assets c WHERE c.parent_id = d.id AND c.role = 'live-video'))
  AND (sqlc.narg(raw)::bool IS NULL OR sqlc.narg(raw)::bool = (dn.name ~* '\.(dng|nef|cr2|arw)$'))
  AND (sqlc.narg(favorite)::bool IS NULL OR d.favorite = sqlc.narg(favorite)::bool)
  AND (sqlc.narg(source_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM photo_sources ps JOIN nodes fn ON fn.id = ps.folder_node_id
        WHERE ps.id = sqlc.narg(source_id)::uuid AND ps.user_id = d.user_id
          AND starts_with(dn.disk_path, fn.disk_path || '/')))
  AND (sqlc.narg(area_z)::int IS NULL OR (d.lat IS NOT NULL AND d.lon IS NOT NULL
        AND photo_map_key(d.lat, d.lon, sqlc.narg(area_z)::int) = ANY(sqlc.narg(area_keys)::bigint[])))))
ORDER BY a.taken_at DESC, a.id;

-- name: ListHiddenPhotoAssets :many
SELECT a.* FROM photo_assets a
WHERE a.user_id = sqlc.arg(user_id)
  AND a.taken_local >= sqlc.arg(month)::timestamp
  AND a.taken_local < sqlc.arg(month)::timestamp + interval '1 month'
  AND a.parent_id IS NULL AND a.hidden AND a.trashed_at IS NULL
ORDER BY a.taken_at DESC, a.id;

-- name: ListTrashedPhotoAssets :many
SELECT a.* FROM photo_assets a
WHERE a.user_id = $1 AND a.parent_id IS NULL AND a.trashed_at IS NOT NULL
ORDER BY a.trashed_at DESC, a.id;

-- name: ListOnThisDay :many
SELECT a.* FROM photo_assets a
WHERE a.user_id = sqlc.arg(user_id)
  AND extract(month from a.taken_local) = sqlc.arg(month)::int
  AND extract(day from a.taken_local) = sqlc.arg(day)::int
  AND a.parent_id IS NULL AND NOT a.hidden AND a.trashed_at IS NULL
ORDER BY a.taken_local DESC, a.id;

-- Takes pending thumbnails, oldest claim first, and stamps indexed_at so later claims
-- rotate to other rows (a stuck row does not starve the rest). The row stays 'pending'
-- until SetThumbState. exclude lists the ids this process is rendering right now: a
-- claim commits at once, so SKIP LOCKED alone would hand the same row to a second worker.
-- name: ClaimPendingThumbs :many
UPDATE photo_assets SET thumb_state = 'pending', indexed_at = now()
WHERE id IN (
    SELECT p.id FROM photo_assets p
    WHERE p.thumb_state = 'pending' AND p.id <> ALL(COALESCE(sqlc.arg(exclude)::uuid[], '{}'))
    ORDER BY p.indexed_at NULLS FIRST, p.id
    LIMIT sqlc.arg(max_rows)
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: SetThumbState :exec
UPDATE photo_assets SET thumb_state = $2, thumb_error = $3 WHERE id = $1;

-- Videos waiting for the web-playable copy. The row stays 'pending' until
-- SetTranscodeState; exclude is what this process is transcoding right now.
-- name: ClaimPendingTranscodes :many
SELECT * FROM photo_assets
WHERE kind = 'video' AND transcode_state = 'pending' AND id <> ALL(COALESCE(sqlc.arg(exclude)::uuid[], '{}'))
ORDER BY id
LIMIT sqlc.arg(max_rows);

-- name: SetTranscodeState :exec
UPDATE photo_assets SET transcode_state = $2 WHERE id = $1;

-- Queues a video for transcoding unless it is queued or done already (a changed file
-- resets the state in UpsertPhotoAsset). Reports whether a row was queued.
-- name: QueuePhotoTranscode :execrows
UPDATE photo_assets SET transcode_state = 'pending'
WHERE id = $1 AND kind = 'video' AND transcode_state IS DISTINCT FROM 'pending'
  AND transcode_state IS DISTINCT FROM 'ready';

-- A nil favorite or hidden leaves that flag as it is.
-- name: SetPhotoFlags :exec
UPDATE photo_assets SET
    favorite = COALESCE(sqlc.narg(favorite)::bool, favorite),
    hidden = COALESCE(sqlc.narg(hidden)::bool, hidden)
WHERE user_id = sqlc.arg(user_id) AND id = ANY(sqlc.arg(ids)::uuid[]);

-- Everything grouping looks at (Live Photo pairs by live_cid, RAW+JPEG by name and time).
-- name: ListPhotoGroupCandidates :many
SELECT a.id, a.node_id, a.kind, a.live_cid, a.parent_id, a.role, a.taken_at, a.taken_local,
       a.duration_ms, a.trashed_at, n.name, n.parent_id AS folder_id
FROM photo_assets a JOIN nodes n ON n.id = a.node_id
WHERE a.user_id = $1;

-- name: SetPhotoParent :exec
UPDATE photo_assets SET parent_id = sqlc.narg(parent_id), role = sqlc.narg(role)
WHERE id = sqlc.arg(id);

-- Assets with coordinates and no place yet. A failed lookup must be stored as 0 by the
-- caller, or the row is picked again forever.
-- name: ListPhotoAssetsWithoutPlace :many
SELECT * FROM photo_assets
WHERE lat IS NOT NULL AND lon IS NOT NULL AND place_id IS NULL
ORDER BY id LIMIT $1;

-- name: SetPhotoPlace :exec
UPDATE photo_assets SET place_id = $2, country_code = $3 WHERE id = $1;

-- name: CountPhotoIndexing :one
SELECT count(*)::bigint AS total,
       count(*) FILTER (WHERE thumb_state = 'ready')::bigint AS ready,
       count(*) FILTER (WHERE thumb_state = 'failed')::bigint AS failed,
       count(*) FILTER (WHERE (thumb_state = 'failed' OR index_error IS NOT NULL) AND NOT hidden)::bigint AS failed_files
FROM photo_assets WHERE user_id = $1;

-- Files that could not be processed (thumbnail or metadata), for the settings list.
-- Hidden ones are left out: their names show only after the unlock.
-- name: ListPhotoFailedFiles :many
SELECT a.id, n.name, n.disk_path, a.thumb_state, a.thumb_error, a.index_error
FROM photo_assets a JOIN nodes n ON n.id = a.node_id
WHERE a.user_id = sqlc.arg(user_id) AND (a.thumb_state = 'failed' OR a.index_error IS NOT NULL)
  AND NOT a.hidden
ORDER BY n.disk_path, a.id
LIMIT sqlc.arg(max_rows) OFFSET sqlc.arg(skip);

-- Other live cards with the same content as the given cards (copies in other folders),
-- by the card they copy. An edit applies to them too, as flags and delete do: to the
-- user they are one photo, and an edited twin would otherwise come back with the old values.
-- name: ListPhotoCardCopies :many
SELECT s.id AS card_id, a.id AS copy_id
FROM photo_assets s
JOIN photo_assets a ON a.user_id = s.user_id AND a.content_hash = s.content_hash AND a.id <> s.id
WHERE s.user_id = sqlc.arg(user_id) AND s.id = ANY(sqlc.arg(ids)::uuid[]) AND s.content_hash IS NOT NULL
  AND a.parent_id IS NULL AND a.trashed_at IS NULL
ORDER BY s.id, a.id;

-- The file changed only in ways the gallery does not read (renamed, trashed and restored:
-- each bumps the node version): record the version without a re-read or a new thumbnail.
-- name: SetPhotoNodeVersion :exec
UPDATE photo_assets SET node_version = $2, indexed_at = now() WHERE node_id = $1;

-- A trashed folder: its assets go to the gallery trash, not away. Only rows whose node
-- is trashed: a live folder created at the same path since then keeps its photos.
-- name: TrashPhotoAssetsUnderPath :exec
UPDATE photo_assets a SET trashed_at = now()
FROM nodes n
WHERE a.user_id = sqlc.arg(user_id) AND n.id = a.node_id AND a.trashed_at IS NULL
  AND n.deleted_at IS NOT NULL
  AND n.disk_path LIKE sqlc.arg(prefix)::text || '/%';

-- A restored folder: its assets come back from the gallery trash.
-- name: UntrashPhotoAssetsUnderPath :exec
UPDATE photo_assets a SET trashed_at = NULL
FROM nodes n
WHERE a.user_id = sqlc.arg(user_id) AND n.id = a.node_id AND a.trashed_at IS NOT NULL
  AND n.deleted_at IS NULL
  AND n.disk_path LIKE sqlc.arg(prefix)::text || '/%';

-- Realigns trashed_at with the files' trash state (a full pass; the change log does it
-- per file).
-- name: SyncPhotoTrash :exec
UPDATE photo_assets a SET trashed_at = CASE WHEN n.deleted_at IS NULL THEN NULL ELSE now() END
FROM nodes n
WHERE a.user_id = $1 AND n.id = a.node_id
  AND (n.deleted_at IS NULL) <> (a.trashed_at IS NULL);

-- Assets whose node is under none of the user's sources (trashed nodes included: a
-- trashed photo of a source stays in the gallery trash).
-- name: PrunePhotoAssetsOutsideSources :many
WITH RECURSIVE subtree AS (
    SELECT ps.folder_node_id AS id FROM photo_sources ps WHERE ps.user_id = sqlc.arg(user_id)
    UNION
    SELECT n.id FROM nodes n JOIN subtree s ON n.parent_id = s.id
)
DELETE FROM photo_assets a
WHERE a.user_id = sqlc.arg(user_id) AND a.node_id NOT IN (SELECT id FROM subtree)
RETURNING a.id;

-- name: ListPhotoAssetIDs :many
SELECT id FROM photo_assets WHERE user_id = $1;

-- The newest purge of the user (0 when none): purged nodes take their asset rows with
-- them (cascade) without a change_log row, so derivative files are swept when it moves.
-- name: MaxPurgedSeq :one
SELECT COALESCE(max(seq), 0)::bigint FROM purged_nodes WHERE user_id = $1;

-- Assets under a folder path whose node is under none of the given roots (the source
-- paths): the folder left the sources. Assets still under a root survive (an ancestor
-- of a source was renamed, moved or restored).
-- name: DeletePhotoAssetsUnderPathOutsideRoots :many
DELETE FROM photo_assets a USING nodes n
WHERE a.user_id = sqlc.arg(user_id) AND n.id = a.node_id
  AND n.disk_path LIKE sqlc.arg(prefix)::text || '/%'
  AND NOT EXISTS (SELECT 1 FROM unnest(sqlc.arg(roots)::text[]) AS r(path)
                  WHERE starts_with(n.disk_path, r.path || '/'))
RETURNING a.id;

-- Children (live videos, originals) of the given parent assets, for the feed rows and the card.
-- name: ListPhotoChildren :many
SELECT id, parent_id, role, node_id FROM photo_assets
WHERE parent_id = ANY(sqlc.arg(parent_ids)::uuid[]) AND role IS NOT NULL
ORDER BY parent_id, role, id;

-- The row by id alone, for endpoints whose credential is a node-scoped token and that
-- compare the owner themselves.
-- name: GetPhotoAssetByID :one
SELECT * FROM photo_assets WHERE id = $1;

-- A card's flags apply to its children (live video, original) and to the user's cards of
-- the same content (copies): to the user they are one photo.
-- name: SetPhotoCardFlags :exec
WITH roots AS (
    SELECT a.id FROM photo_assets a
    WHERE a.user_id = sqlc.arg(user_id)
      AND (a.id = ANY(sqlc.arg(ids)::uuid[])
           OR (a.parent_id IS NULL AND a.content_hash IS NOT NULL AND a.content_hash IN (
                SELECT s.content_hash FROM photo_assets s
                WHERE s.user_id = sqlc.arg(user_id) AND s.id = ANY(sqlc.arg(ids)::uuid[]))))
)
UPDATE photo_assets AS t SET
    favorite = COALESCE(sqlc.narg(favorite)::bool, t.favorite),
    hidden = COALESCE(sqlc.narg(hidden)::bool, t.hidden)
WHERE t.user_id = sqlc.arg(user_id)
  AND (t.id IN (SELECT r.id FROM roots r) OR t.parent_id IN (SELECT r.id FROM roots r));

-- The caller's own assets by id, any state: the handlers decide what a hidden or
-- trashed one may do.
-- name: ListOwnPhotoAssets :many
SELECT * FROM photo_assets WHERE user_id = $1 AND id = ANY(sqlc.arg(ids)::uuid[]);

-- Other live assets with the same content in the user's sources (copies of the card).
-- name: CountPhotoCopies :one
SELECT count(*)::bigint FROM photo_assets a
WHERE a.user_id = $1 AND a.id <> $2 AND a.trashed_at IS NULL AND a.parent_id IS NULL
  AND a.content_hash IS NOT NULL
  AND a.content_hash = (SELECT content_hash FROM photo_assets WHERE id = $2 AND user_id = $1);

-- Nodes to delete for cards: each card, its children, and every copy of the same
-- content (with the copies' children).
-- name: ListPhotoDeleteNodes :many
WITH roots AS (
    SELECT a.id FROM photo_assets a
    WHERE a.user_id = sqlc.arg(user_id) AND a.trashed_at IS NULL
      AND (a.id = ANY(sqlc.arg(ids)::uuid[])
           OR (a.parent_id IS NULL AND a.content_hash IS NOT NULL AND a.content_hash IN (
                SELECT s.content_hash FROM photo_assets s
                WHERE s.user_id = sqlc.arg(user_id) AND s.id = ANY(sqlc.arg(ids)::uuid[]))))
)
SELECT DISTINCT a.node_id FROM photo_assets a
WHERE a.user_id = sqlc.arg(user_id) AND a.trashed_at IS NULL
  AND (a.id IN (SELECT id FROM roots) OR a.parent_id IN (SELECT id FROM roots));

-- Trashed nodes to restore for cards: each card and its children.
-- name: ListPhotoRestoreNodes :many
SELECT DISTINCT a.node_id FROM photo_assets a
WHERE a.user_id = sqlc.arg(user_id) AND a.trashed_at IS NOT NULL
  AND (a.id = ANY(sqlc.arg(ids)::uuid[]) OR a.parent_id = ANY(sqlc.arg(ids)::uuid[]));

-- Months of the hidden cards, as the timeline's buckets.
-- name: ListHiddenPhotoMonthBuckets :many
SELECT date_trunc('month', a.taken_local)::date AS month, count(*)::bigint AS count
FROM photo_assets a
WHERE a.user_id = $1 AND a.parent_id IS NULL AND a.hidden AND a.trashed_at IS NULL
GROUP BY 1
ORDER BY 1 DESC;

-- The capture date of the node was set by the user (written into the file or its sidecar).
-- name: SetPhotoDateEdited :exec
UPDATE photo_assets SET date_source = 'edit' WHERE node_id = $1;

-- The user erased (true) or set (false) the position by hand; erasing also clears what is
-- stored, so the row is right before the next re-read.
-- name: SetPhotoPlaceCleared :exec
UPDATE photo_assets SET place_cleared = sqlc.arg(cleared)::bool,
    lat = CASE WHEN sqlc.arg(cleared)::bool THEN NULL ELSE lat END,
    lon = CASE WHEN sqlc.arg(cleared)::bool THEN NULL ELSE lon END,
    place_id = CASE WHEN sqlc.arg(cleared)::bool THEN NULL ELSE place_id END,
    country_code = CASE WHEN sqlc.arg(cleared)::bool THEN NULL ELSE country_code END
WHERE node_id = sqlc.arg(node_id);

-- Which of the hashes the user's gallery already holds, trashed and hidden files included.
-- name: ListKnownPhotoHashes :many
SELECT DISTINCT content_hash::text FROM photo_assets
WHERE user_id = sqlc.arg(user_id) AND content_hash = ANY(sqlc.arg(hashes)::text[]);

-- The map's clusters: the feed's cards with a position inside the box, grouped by
-- photo_map_cell at zoom z. Duplicates count once, as in the feed. A west edge east of the
-- east edge means the box crosses the 180th meridian. place_id and country_code are set
-- only when every card of the cluster shares them (0 and '' otherwise); the cover is the
-- newest card.
-- name: ListPhotoMapClusters :many
WITH f AS (
    SELECT a.id, a.lat, a.lon, a.taken_at, a.place_id, a.country_code, a.content_hash,
           photo_map_cell(a.lat, a.lon, sqlc.arg(z)::int) AS cell
    FROM photo_assets a
    WHERE a.user_id = sqlc.arg(user_id)
      AND a.parent_id IS NULL AND NOT a.hidden AND a.trashed_at IS NULL
      AND a.lat IS NOT NULL AND a.lon IS NOT NULL
      AND a.lat BETWEEN sqlc.arg(south)::float8 AND sqlc.arg(north)::float8
      AND CASE WHEN sqlc.arg(west)::float8 <= sqlc.arg(east)::float8
               THEN a.lon BETWEEN sqlc.arg(west)::float8 AND sqlc.arg(east)::float8
               ELSE a.lon >= sqlc.arg(west)::float8 OR a.lon <= sqlc.arg(east)::float8 END
), ranked AS (
    SELECT f.*, row_number() OVER (PARTITION BY content_hash ORDER BY id) AS rn FROM f
)
SELECT cell[1]::bigint AS x, cell[2]::bigint AS y, count(*)::bigint AS count,
       avg(lat)::float8 AS lat, avg(lon)::float8 AS lon,
       (array_agg(id ORDER BY taken_at DESC NULLS LAST, id))[1]::uuid AS cover_id,
       coalesce(CASE WHEN count(DISTINCT place_id) = 1 AND count(place_id) = count(*) THEN min(place_id) END, 0)::int AS place_id,
       coalesce(CASE WHEN count(DISTINCT country_code) = 1 AND count(country_code) = count(*) THEN min(country_code) END, '')::text AS country_code
FROM ranked
WHERE content_hash IS NULL OR rn = 1
GROUP BY cell
ORDER BY 3 DESC, 1, 2;

-- The box around every card of the map (the first view); no row when none has a position.
-- name: GetPhotoMapBounds :one
SELECT min(lon)::float8 AS west, min(lat)::float8 AS south, max(lon)::float8 AS east, max(lat)::float8 AS north
FROM photo_assets
WHERE user_id = $1 AND parent_id IS NULL AND NOT hidden AND trashed_at IS NULL
  AND lat IS NOT NULL AND lon IS NOT NULL
HAVING count(*) > 0;
