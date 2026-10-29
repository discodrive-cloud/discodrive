-- Gallery sharing: partners, albums, album links (migration 29).

-- name: AddPhotoPartner :exec
INSERT INTO photo_partners (owner_id, viewer_id) VALUES ($1, $2)
ON CONFLICT (owner_id, viewer_id) DO NOTHING;

-- name: DeletePhotoPartner :execrows
DELETE FROM photo_partners WHERE owner_id = $1 AND viewer_id = $2;

-- name: SetPhotoPartnerFeed :execrows
UPDATE photo_partners SET in_feed = $3 WHERE owner_id = $1 AND viewer_id = $2;

-- name: ListPhotoPartnerViewers :many
-- Whom the owner opened the gallery to.
SELECT u.id, u.email, p.created_at FROM photo_partners p
JOIN users u ON u.id = p.viewer_id
WHERE p.owner_id = $1
ORDER BY u.email;

-- name: ListPhotoPartnerOwners :many
-- Whose galleries are open to the viewer.
SELECT u.id, u.email, p.in_feed, p.created_at FROM photo_partners p
JOIN users u ON u.id = p.owner_id
WHERE p.viewer_id = $1
ORDER BY u.email;

-- name: IsPhotoPartner :one
SELECT EXISTS (SELECT 1 FROM photo_partners WHERE owner_id = $1 AND viewer_id = $2);

-- name: GetViewablePhotoAsset :one
-- Somebody else's card (or a part of it) the viewer may look at: the owner's partner sees
-- cards whose file is the owner's own; album members see the album's cards. Hidden and
-- trashed cards are seen by nobody but their owner.
SELECT a.* FROM photo_assets a
JOIN photo_assets c ON c.id = coalesce(a.parent_id, a.id)
JOIN nodes cn ON cn.id = c.node_id
WHERE a.id = sqlc.arg(id) AND a.trashed_at IS NULL
  AND NOT c.hidden AND c.trashed_at IS NULL
  AND ((cn.user_id = c.user_id AND EXISTS (
          SELECT 1 FROM photo_partners p JOIN photo_settings s ON s.user_id = p.owner_id AND s.enabled
          WHERE p.owner_id = c.user_id AND p.viewer_id = sqlc.arg(viewer_id)))
    OR EXISTS (
          SELECT 1 FROM photo_album_visible_items i JOIN photo_albums al ON al.id = i.album_id
          WHERE i.asset_id = c.id AND (al.owner_id = sqlc.arg(viewer_id) OR EXISTS (
              SELECT 1 FROM photo_album_members m WHERE m.album_id = al.id AND m.user_id = sqlc.arg(viewer_id)))));

-- name: GetAlbumPhotoAsset :one
-- A card of the album (or a part of one), for its public link.
SELECT a.* FROM photo_assets a
JOIN photo_assets c ON c.id = coalesce(a.parent_id, a.id)
WHERE a.id = sqlc.arg(id) AND a.trashed_at IS NULL
  AND NOT c.hidden AND c.trashed_at IS NULL
  AND EXISTS (SELECT 1 FROM photo_album_visible_items i WHERE i.album_id = sqlc.arg(album_id) AND i.asset_id = c.id);

-- name: ListPhotoAssetsByIDs :many
SELECT * FROM photo_assets WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CreatePhotoAlbum :one
INSERT INTO photo_albums (owner_id, name) VALUES ($1, $2) RETURNING *;

-- name: GetPhotoAlbum :one
SELECT * FROM photo_albums WHERE id = $1;

-- name: GetPhotoAlbumRole :one
-- 'owner', 'member' or '' for the user and the album.
SELECT CASE WHEN al.owner_id = sqlc.arg(user_id) THEN 'owner'
            WHEN EXISTS (SELECT 1 FROM photo_album_members m WHERE m.album_id = al.id AND m.user_id = sqlc.arg(user_id)) THEN 'member'
            ELSE '' END::text AS role
FROM photo_albums al WHERE al.id = sqlc.arg(id);

-- name: ListPhotoAlbumsFor :many
-- The albums a user owns or is a member of, with what they show: visible cards (each
-- file once) and the latest of them as the cover.
WITH mine AS (
    SELECT al.* FROM photo_albums al
    WHERE al.owner_id = sqlc.arg(user_id)
       OR EXISTS (SELECT 1 FROM photo_album_members m WHERE m.album_id = al.id AND m.user_id = sqlc.arg(user_id))
), shown AS (
    SELECT i.album_id, a.id, a.taken_at,
           row_number() OVER (PARTITION BY i.album_id, coalesce(a.content_hash, a.id::text) ORDER BY a.id) AS rn
    FROM photo_album_visible_items i JOIN photo_assets a ON a.id = i.asset_id
    WHERE i.album_id IN (SELECT id FROM mine)
      AND a.parent_id IS NULL AND NOT a.hidden AND a.trashed_at IS NULL
)
SELECT m.id, m.owner_id, m.name, m.created_at, m.updated_at, u.email AS owner_email,
       (SELECT count(*) FROM shown s WHERE s.album_id = m.id AND s.rn = 1)::bigint AS count,
       (SELECT s.id FROM shown s WHERE s.album_id = m.id AND s.rn = 1
        ORDER BY s.taken_at DESC, s.id LIMIT 1)::uuid AS cover_id,
       (SELECT count(*) FROM photo_album_members mm WHERE mm.album_id = m.id)::bigint AS members
FROM mine m JOIN users u ON u.id = m.owner_id
ORDER BY m.updated_at DESC, m.id;

-- name: RenamePhotoAlbum :execrows
UPDATE photo_albums SET name = $2, updated_at = now() WHERE id = $1;

-- name: TouchPhotoAlbum :exec
UPDATE photo_albums SET updated_at = now() WHERE id = $1;

-- name: DeletePhotoAlbum :execrows
DELETE FROM photo_albums WHERE id = $1;

-- name: AddPhotoAlbumItems :execrows
INSERT INTO photo_album_items (album_id, asset_id, added_by)
SELECT sqlc.arg(album_id), unnest(sqlc.arg(asset_ids)::uuid[]), sqlc.arg(added_by)
ON CONFLICT (album_id, asset_id) DO NOTHING;

-- name: RemovePhotoAlbumItems :execrows
-- The owner takes out anything; a member what they added.
DELETE FROM photo_album_items
WHERE album_id = sqlc.arg(album_id) AND asset_id = ANY(sqlc.arg(asset_ids)::uuid[])
  AND (sqlc.arg(any_item)::bool OR added_by = sqlc.arg(user_id));

-- name: AddPhotoAlbumMember :exec
INSERT INTO photo_album_members (album_id, user_id) VALUES ($1, $2)
ON CONFLICT (album_id, user_id) DO NOTHING;

-- name: DeletePhotoAlbumMember :execrows
DELETE FROM photo_album_members WHERE album_id = $1 AND user_id = $2;

-- name: ListPhotoAlbumMembers :many
SELECT u.id, u.email, m.created_at FROM photo_album_members m
JOIN users u ON u.id = m.user_id
WHERE m.album_id = $1
ORDER BY u.email;

-- name: ListPhotoAlbumMonthBuckets :many
WITH f AS (
    SELECT a.taken_local,
           row_number() OVER (PARTITION BY coalesce(a.content_hash, a.id::text) ORDER BY a.id) AS rn
    FROM photo_album_visible_items i JOIN photo_assets a ON a.id = i.asset_id
    WHERE i.album_id = $1 AND a.parent_id IS NULL AND NOT a.hidden AND a.trashed_at IS NULL
)
SELECT date_trunc('month', taken_local)::date AS month, count(*)::bigint AS count
FROM f WHERE rn = 1
GROUP BY 1
ORDER BY 1 DESC;

-- name: ListPhotoAlbumAssetsInMonth :many
SELECT a.* FROM photo_album_visible_items i JOIN photo_assets a ON a.id = i.asset_id
WHERE i.album_id = sqlc.arg(album_id)
  AND a.taken_local >= sqlc.arg(month)::timestamp
  AND a.taken_local < sqlc.arg(month)::timestamp + interval '1 month'
  AND a.parent_id IS NULL AND NOT a.hidden AND a.trashed_at IS NULL
  AND (a.content_hash IS NULL OR NOT EXISTS (
        SELECT 1 FROM photo_album_visible_items j JOIN photo_assets d ON d.id = j.asset_id
        WHERE j.album_id = i.album_id AND d.content_hash = a.content_hash AND d.id < a.id
          AND d.parent_id IS NULL AND NOT d.hidden AND d.trashed_at IS NULL))
ORDER BY a.taken_at DESC, a.id;

-- name: ListPhotoAlbumAssetsPage :many
-- The whole album newest first, a page after (taken_at, id), for its public page.
SELECT a.* FROM photo_album_visible_items i JOIN photo_assets a ON a.id = i.asset_id
WHERE i.album_id = sqlc.arg(album_id)
  AND a.parent_id IS NULL AND NOT a.hidden AND a.trashed_at IS NULL
  AND (a.content_hash IS NULL OR NOT EXISTS (
        SELECT 1 FROM photo_album_visible_items j JOIN photo_assets d ON d.id = j.asset_id
        WHERE j.album_id = i.album_id AND d.content_hash = a.content_hash AND d.id < a.id
          AND d.parent_id IS NULL AND NOT d.hidden AND d.trashed_at IS NULL))
  AND (sqlc.narg(after_taken)::timestamptz IS NULL
       OR (a.taken_at, a.id) < (sqlc.narg(after_taken)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY a.taken_at DESC, a.id DESC
LIMIT sqlc.arg(lim);

-- name: CreatePhotoAlbumLink :one
INSERT INTO photo_album_links (album_id, token, password_hash, expires_at, allow_download)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: ListPhotoAlbumLinks :many
SELECT * FROM photo_album_links WHERE album_id = $1 ORDER BY created_at;

-- name: DeletePhotoAlbumLink :execrows
DELETE FROM photo_album_links WHERE id = $1 AND album_id = $2;

-- name: GetActivePhotoAlbumLink :one
SELECT l.*, al.name AS album_name FROM photo_album_links l
JOIN photo_albums al ON al.id = l.album_id
WHERE l.token = $1 AND (l.expires_at IS NULL OR l.expires_at > now());

-- name: HasPhotoSharesFor :one
-- Whether somebody opened a gallery or an album to the user.
SELECT (EXISTS (SELECT 1 FROM photo_partners WHERE viewer_id = $1)
     OR EXISTS (SELECT 1 FROM photo_album_members WHERE user_id = $1))::bool;
