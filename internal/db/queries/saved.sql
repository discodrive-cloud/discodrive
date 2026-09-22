-- Saved items: read-later articles and server-side downloads (internal queue).

-- name: UpsertSavedItem :one
-- Re-save refreshes the client-supplied HTML/cookies (a newer browser state)
-- but never resets the status.
INSERT INTO saved_items (user_id, url, kind, title, content_html, cookie_header, cookie_expires_at)
VALUES ($1, $2, $3, $4, sqlc.narg(content_html), sqlc.narg(cookie_header),
 CASE WHEN sqlc.narg(cookie_header)::text IS NOT NULL THEN now() + interval '15 minutes' END)
ON CONFLICT (user_id, url, kind) DO UPDATE SET
    content_html = COALESCE(EXCLUDED.content_html, saved_items.content_html),
    cookie_header = CASE WHEN saved_items.status IN ('pending', 'error') THEN EXCLUDED.cookie_header END,
    cookie_expires_at = CASE WHEN saved_items.status IN ('pending', 'error') THEN EXCLUDED.cookie_expires_at WHEN saved_items.status = 'processing' THEN saved_items.cookie_expires_at END,
    updated_at = now()
RETURNING *;

-- name: ListSavedItems :many
-- Downloads are an internal queue and never appear in listings.
SELECT * FROM saved_items
WHERE user_id = $1
  AND kind <> 'download'
  AND (sqlc.arg(kind)::text = '' OR kind = sqlc.arg(kind)::text)
  AND (sqlc.arg(status)::text = '' OR status = sqlc.arg(status)::text)
  AND (sqlc.arg(q)::text = '' OR title ILIKE '%' || sqlc.arg(q)::text || '%' OR url ILIKE '%' || sqlc.arg(q)::text || '%')
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: GetSavedItemForUser :one
SELECT * FROM saved_items WHERE id = $1 AND user_id = $2;

-- name: ClaimSavedItem :execrows
UPDATE saved_items SET status = 'processing', error_msg = '', cookie_header = NULL, updated_at = now()
WHERE id = $1 AND status = 'pending';

-- name: UpdateSavedItemProgress :execrows
UPDATE saved_items
SET bytes_done = $2, size_bytes = COALESCE(sqlc.narg(size_bytes), size_bytes), updated_at = now()
WHERE id = $1 AND status = 'processing';

-- name: SetSavedItemDone :execrows
-- content_html/cookie_header are dropped once processed — transport, not storage.
UPDATE saved_items
SET status = 'done', content_path = $2, size_bytes = $3, bytes_done = COALESCE($3, bytes_done),
    title = COALESCE(NULLIF(sqlc.arg(title)::text, ''), title), meta = $4,
    content_html = NULL, cookie_header = NULL, cookie_expires_at = NULL, updated_at = now()
WHERE id = $1 AND status = 'processing';

-- name: SetSavedItemError :exec
UPDATE saved_items SET status = 'error', error_msg = $2, cookie_header = NULL, cookie_expires_at = NULL, updated_at = now()
WHERE id = $1;

-- name: RetrySavedItem :execrows
UPDATE saved_items SET status = 'pending', error_msg = '', bytes_done = 0, updated_at = now()
WHERE id = $1 AND user_id = $2 AND status IN ('error', 'done');

-- name: DeleteSavedItemForUser :one
DELETE FROM saved_items WHERE id = $1 AND user_id = $2
RETURNING kind, content_path, meta;

-- name: ListPendingSavedItems :many
SELECT * FROM saved_items WHERE status = 'pending' ORDER BY created_at LIMIT $1;

-- name: ResetStaleSavedItems :execrows
UPDATE saved_items SET
 status = CASE WHEN cookie_expires_at IS NOT NULL THEN 'error' ELSE 'pending' END,
 error_msg = CASE WHEN cookie_expires_at IS NOT NULL THEN 'Please submit the authenticated download again' ELSE '' END,
 cookie_header = NULL, cookie_expires_at = NULL, bytes_done = 0, updated_at = now()
WHERE status = 'processing';

-- name: DeleteFinishedDownloads :execrows
DELETE FROM saved_items WHERE kind = 'download'
  AND ((status = 'done' AND updated_at < $1) OR (status = 'error' AND updated_at < $2));

-- name: ClearExpiredSavedCookies :exec
UPDATE saved_items SET cookie_header = NULL, cookie_expires_at = NULL,
    status = 'error', error_msg = 'Download credentials expired; submit the download again'
WHERE cookie_header IS NOT NULL AND cookie_expires_at <= now();
