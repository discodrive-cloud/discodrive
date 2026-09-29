-- Bookmarks: server-authoritative tree with per-user seq cursor + tombstones.
-- Every mutation bumps users.bookmark_seq via a CTE in the same statement: the
-- row lock on users serializes a user's mutations, so per-user commit order
-- equals seq order and a committed bookmark_seq is always a safe cursor.

-- name: ListBrowserBookmarks :many
SELECT * FROM browser_bookmarks
WHERE user_id = $1 AND NOT deleted
ORDER BY parent_id NULLS FIRST, position, title;

-- name: GetBrowserBookmarkForUser :one
SELECT * FROM browser_bookmarks WHERE id = $1 AND user_id = $2;

-- name: GetBookmarkSyncState :one
SELECT bookmark_seq, bookmark_gc_seq FROM users WHERE id = $1;

-- name: ListBrowserBookmarkChanges :many
SELECT * FROM browser_bookmarks
WHERE user_id = $1 AND seq > $2
ORDER BY seq
LIMIT $3;

-- Every row of one change group. A bulk import or a folder delete stamps all its rows
-- with a single seq, so a page cut inside a group must be completed from here — a
-- cursor at that seq would otherwise skip the rest of the group.
-- name: ListBrowserBookmarksAtSeq :many
SELECT * FROM browser_bookmarks
WHERE user_id = $1 AND seq = $2
ORDER BY id;

-- name: NextBookmarkSeq :one
UPDATE users SET bookmark_seq = bookmark_seq + 1 WHERE id = $1 RETURNING bookmark_seq;

-- name: CreateBrowserBookmark :one
WITH s AS (
    UPDATE users SET bookmark_seq = bookmark_seq + 1 WHERE id = $1 RETURNING bookmark_seq
)
INSERT INTO browser_bookmarks (id, user_id, parent_id, is_folder, title, url, position, seq)
VALUES (COALESCE(sqlc.narg(id)::uuid, gen_random_uuid()), $1, sqlc.narg(parent_id), $2, $3, $4, $5,
        (SELECT bookmark_seq FROM s))
ON CONFLICT (id) DO NOTHING
RETURNING *;

-- name: UpdateBrowserBookmark :one
WITH s AS (
    UPDATE users SET bookmark_seq = bookmark_seq + 1 WHERE id = $1 RETURNING bookmark_seq
)
UPDATE browser_bookmarks SET
    title = COALESCE(sqlc.narg(title), title),
    url = COALESCE(sqlc.narg(url), url),
    position = COALESCE(sqlc.narg(position), position),
    seq = (SELECT bookmark_seq FROM s),
    updated_at = now()
WHERE browser_bookmarks.id = $2 AND browser_bookmarks.user_id = $1 AND NOT deleted
RETURNING *;

-- MoveBookmark re-parents a node. The NOT EXISTS guard rejects a move that
-- would create a cycle (the target parent must not be inside the moved subtree).
-- name: MoveBrowserBookmark :one
WITH RECURSIVE s AS (
    UPDATE users SET bookmark_seq = bookmark_seq + 1 WHERE id = $1 RETURNING bookmark_seq
), sub AS (
    SELECT b.id FROM browser_bookmarks b WHERE b.id = $2 AND b.user_id = $1
    UNION ALL
    SELECT c.id FROM browser_bookmarks c JOIN sub ON c.parent_id = sub.id WHERE c.user_id = $1
)
UPDATE browser_bookmarks SET
    parent_id = sqlc.narg(parent_id),
    seq = (SELECT bookmark_seq FROM s),
    updated_at = now()
WHERE browser_bookmarks.id = $2 AND browser_bookmarks.user_id = $1 AND NOT deleted
  AND NOT EXISTS (SELECT 1 FROM sub WHERE sub.id = sqlc.narg(parent_id))
RETURNING *;

-- TombstoneBookmarkTree marks the whole subtree deleted with one seq value
-- (valid: the cursor needs per-row monotonicity, not uniqueness). Runs inside
-- a tx after NextBookmarkSeq.
-- name: TombstoneBrowserBookmarkTree :execrows
WITH RECURSIVE sub AS (
    SELECT b.id FROM browser_bookmarks b WHERE b.id = $2 AND b.user_id = $1
    UNION ALL
    SELECT c.id FROM browser_bookmarks c JOIN sub ON c.parent_id = sub.id WHERE c.user_id = $1
)
UPDATE browser_bookmarks SET deleted = true, seq = $3, updated_at = now()
WHERE id IN (SELECT id FROM sub) AND NOT deleted;

-- UpsertBookmarkAt is the bulk-import step (tx, seq passed in). LWW: an
-- existing row (including a tombstone) is overwritten and revived.
-- name: UpsertBrowserBookmarkAt :exec
INSERT INTO browser_bookmarks (id, user_id, parent_id, is_folder, title, url, position, seq)
VALUES ($1, $2, sqlc.narg(parent_id), $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
    parent_id = EXCLUDED.parent_id,
    is_folder = EXCLUDED.is_folder,
    title = EXCLUDED.title,
    url = EXCLUDED.url,
    position = EXCLUDED.position,
    deleted = false,
    seq = EXCLUDED.seq,
    updated_at = now()
WHERE browser_bookmarks.user_id = EXCLUDED.user_id;

-- name: ListBrowserBookmarksNeedingFavicon :many
SELECT * FROM browser_bookmarks
WHERE favicon_tried_at IS NULL AND NOT deleted AND NOT is_folder
ORDER BY created_at
LIMIT $1;

-- SetBookmarkFavicon does NOT bump seq: favicons are server-side decoration,
-- browsers don't need to re-pull the node.
-- name: SetBrowserBookmarkFavicon :exec
UPDATE browser_bookmarks SET favicon_ext = $2, favicon_tried_at = now() WHERE id = $1;

-- name: SetBrowserBookmarkTitleIfEmpty :execrows
WITH s AS (
    UPDATE users SET bookmark_seq = bookmark_seq + 1 WHERE id = $1 RETURNING bookmark_seq
)
UPDATE browser_bookmarks SET title = $3, seq = (SELECT bookmark_seq FROM s), updated_at = now()
WHERE browser_bookmarks.id = $2 AND browser_bookmarks.user_id = $1
  AND browser_bookmarks.title = '' AND NOT deleted;

-- name: GCBrowserBookmarkTombstones :many
DELETE FROM browser_bookmarks
WHERE deleted AND updated_at < $1
RETURNING id, user_id, favicon_ext, seq;

-- name: BumpBookmarkGCSeq :exec
UPDATE users SET bookmark_gc_seq = GREATEST(bookmark_gc_seq, $2) WHERE id = $1;
