-- A finished thumbnail and its hash in one write, so a card is never ready with the hash of
-- its previous picture. phash NULL (the thumbnail could not be hashed) leaves it to the backfill.
-- name: SetThumbReady :exec
UPDATE photo_assets SET thumb_state = 'ready', thumb_error = NULL,
       phash = sqlc.narg(phash), phash_flat = sqlc.arg(phash_flat)
WHERE id = sqlc.arg(id);

-- The backfill's write: only while the card still shows the picture that was hashed (still
-- ready, unhashed, same file version and turn); otherwise a render wrote a fresher hash.
-- name: SetPhotoHash :exec
UPDATE photo_assets SET phash = sqlc.arg(phash), phash_flat = sqlc.arg(phash_flat)
WHERE id = sqlc.arg(id) AND phash IS NULL AND thumb_state = 'ready'
  AND rotation = sqlc.arg(rotation) AND node_version IS NOT DISTINCT FROM sqlc.narg(node_version);

-- Top-level cards whose thumbnail is ready but whose hash is not computed; after_id pages
-- through them so a card that cannot be hashed does not come back within one pass.
-- name: ListPhotoHashBackfill :many
SELECT * FROM photo_assets
WHERE thumb_state = 'ready' AND phash IS NULL AND parent_id IS NULL AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(max_rows);

-- Of the cards with a thumbnail, how many are hashed (flat ones too).
-- name: CountPhotoHashProgress :one
SELECT count(*) FILTER (WHERE thumb_state = 'ready')::bigint AS total,
       count(*) FILTER (WHERE thumb_state = 'ready' AND phash IS NOT NULL)::bigint AS hashed
FROM photo_assets
WHERE user_id = $1 AND parent_id IS NULL AND trashed_at IS NULL AND NOT hidden;

-- One row per byte-identical group of top-level cards (the lowest id that has a ready
-- thumbnail and a usable hash); bytes/copies cover every copy of the content, trashed ones
-- excluded. key names the content for the dismissed pairs.
-- name: ListDupCandidates :many
WITH base AS (
    SELECT a.id, a.kind, a.taken_at, a.taken_local, a.date_source, a.make, a.model, a.width, a.height,
           a.duration_ms, a.phash, a.phash_flat, a.indexed_at, a.thumb_state,
           coalesce(a.content_hash, a.id::text)::text AS key,
           n.name, n.disk_path, coalesce(n.size, 0)::bigint AS size,
           EXISTS (SELECT 1 FROM photo_assets k
                   WHERE k.parent_id = a.id AND k.role IN ('live-video', 'original')
                     AND k.trashed_at IS NULL) AS has_kids,
           row_number() OVER (PARTITION BY coalesce(a.content_hash, a.id::text)
                              ORDER BY (a.thumb_state = 'ready' AND a.phash IS NOT NULL AND NOT a.phash_flat) DESC,
                                       a.id) AS rn,
           sum(coalesce(n.size, 0)) OVER (PARTITION BY coalesce(a.content_hash, a.id::text))::bigint AS total_bytes,
           count(*) OVER (PARTITION BY coalesce(a.content_hash, a.id::text))::int AS copies
    FROM photo_assets a
    JOIN nodes n ON n.id = a.node_id
    WHERE a.user_id = $1 AND a.parent_id IS NULL AND a.trashed_at IS NULL AND NOT a.hidden
      AND n.deleted_at IS NULL
)
SELECT id, kind, taken_at, taken_local, date_source, make, model, width, height, duration_ms, phash,
       indexed_at, key, name, disk_path, total_bytes, copies, has_kids
FROM base
WHERE rn = 1 AND thumb_state = 'ready' AND phash IS NOT NULL AND NOT phash_flat
ORDER BY id;

-- ListDupCandidates for the contents named by keys only: what a resolve or dismiss needs to
-- check the cards it was given, without a pass over the whole library. The windows are
-- per content, so the rows are the ones ListDupCandidates returns for these contents.
-- name: ListDupCandidatesByKeys :many
WITH base AS (
    SELECT a.id, a.kind, a.taken_at, a.taken_local, a.date_source, a.make, a.model, a.width, a.height,
           a.duration_ms, a.phash, a.phash_flat, a.indexed_at, a.thumb_state,
           coalesce(a.content_hash, a.id::text)::text AS key,
           n.name, n.disk_path, coalesce(n.size, 0)::bigint AS size,
           EXISTS (SELECT 1 FROM photo_assets k
                   WHERE k.parent_id = a.id AND k.role IN ('live-video', 'original')
                     AND k.trashed_at IS NULL) AS has_kids,
           row_number() OVER (PARTITION BY coalesce(a.content_hash, a.id::text)
                              ORDER BY (a.thumb_state = 'ready' AND a.phash IS NOT NULL AND NOT a.phash_flat) DESC,
                                       a.id) AS rn,
           sum(coalesce(n.size, 0)) OVER (PARTITION BY coalesce(a.content_hash, a.id::text))::bigint AS total_bytes,
           count(*) OVER (PARTITION BY coalesce(a.content_hash, a.id::text))::int AS copies
    FROM photo_assets a
    JOIN nodes n ON n.id = a.node_id
    WHERE a.user_id = $1 AND a.parent_id IS NULL AND a.trashed_at IS NULL AND NOT a.hidden
      AND coalesce(a.content_hash, a.id::text) = ANY(sqlc.arg(keys)::text[])
      AND n.deleted_at IS NULL
)
SELECT id, kind, taken_at, taken_local, date_source, make, model, width, height, duration_ms, phash,
       indexed_at, key, name, disk_path, total_bytes, copies, has_kids
FROM base
WHERE rn = 1 AND thumb_state = 'ready' AND phash IS NOT NULL AND NOT phash_flat
ORDER BY id;

-- name: ListDupDismissedAmong :many
SELECT a, b FROM photo_dup_dismissed
WHERE user_id = $1 AND a = ANY(sqlc.arg(keys)::text[]) AND b = ANY(sqlc.arg(keys)::text[]);

-- name: ListDupDismissed :many
SELECT a, b FROM photo_dup_dismissed WHERE user_id = $1;

-- name: AddDupDismissed :exec
INSERT INTO photo_dup_dismissed (user_id, a, b) VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;
