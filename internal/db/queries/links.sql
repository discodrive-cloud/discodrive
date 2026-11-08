-- Saved links: the "Share → DiscoDrive" inbox.

-- name: UpsertSavedLink :one
-- Sharing a link again brings it to the top of the inbox, out of the archive too — but
-- only a share made after the archiving: a phone resending a queued copy of an old share
-- must not undo what the user did since. A row the first fetch found nothing for is
-- fetched again; a title already found wins over an empty one.
INSERT INTO saved_links (user_id, url, source, title, saved_at)
VALUES ($1, $2, $3, $4, sqlc.arg(shared_at)::timestamptz)
ON CONFLICT (user_id, url) DO UPDATE SET
    saved_at = GREATEST(saved_links.saved_at, EXCLUDED.saved_at),
    archived_at = CASE WHEN saved_links.archived_at >= EXCLUDED.saved_at THEN saved_links.archived_at END,
    title = CASE WHEN saved_links.title = '' THEN EXCLUDED.title ELSE saved_links.title END,
    enriched_at = CASE WHEN saved_links.title = '' AND saved_links.thumb IS NULL THEN NULL ELSE saved_links.enriched_at END
RETURNING id, user_id, url, source, title, author, (thumb IS NOT NULL)::boolean AS has_thumb,
          enriched_at, archived_at, saved_at, created_at;

-- name: ListSavedLinks :many
SELECT id, user_id, url, source, title, author, (thumb IS NOT NULL)::boolean AS has_thumb,
       enriched_at, archived_at, saved_at, created_at
FROM saved_links
WHERE user_id = $1
  AND (archived_at IS NOT NULL) = sqlc.arg(archived)::boolean
  AND (sqlc.arg(source)::text = '' OR source = sqlc.arg(source)::text)
ORDER BY saved_at DESC
LIMIT $2;

-- name: SavedLinkSources :many
SELECT DISTINCT source FROM saved_links
WHERE user_id = $1 AND (archived_at IS NOT NULL) = sqlc.arg(archived)::boolean
ORDER BY source;

-- name: CountInboxLinks :one
SELECT count(*) FROM saved_links WHERE user_id = $1 AND archived_at IS NULL;

-- name: CountSavedLinks :one
SELECT count(*) FROM saved_links WHERE user_id = $1;

-- name: SavedLinkExists :one
SELECT EXISTS (SELECT 1 FROM saved_links WHERE user_id = $1 AND url = $2);

-- name: GetSavedLinkThumb :one
SELECT thumb FROM saved_links WHERE id = $1 AND user_id = $2 AND thumb IS NOT NULL;

-- name: SetSavedLinkArchived :execrows
UPDATE saved_links
SET archived_at = CASE WHEN sqlc.arg(archived)::boolean THEN COALESCE(archived_at, now()) END
WHERE id = $1 AND user_id = $2;

-- name: DeleteSavedLink :execrows
DELETE FROM saved_links WHERE id = $1 AND user_id = $2;

-- name: ListLinksNeedingEnrich :many
-- Backstop for the fetch started right after saving: only rows that one has had a
-- minute to handle, so the two do not fetch the same page at once. The batch goes round
-- the users, each one's oldest first by when the server stored it: saved_at comes from
-- the phone, so ordering by it would let backdated links take every batch.
SELECT id, user_id, url FROM (
    SELECT id, user_id, url, created_at,
           row_number() OVER (PARTITION BY user_id ORDER BY created_at) AS rn
    FROM saved_links
    WHERE enriched_at IS NULL AND created_at < now() - interval '1 minute'
) pending
ORDER BY rn, created_at LIMIT $1;

-- name: SetSavedLinkEnriched :exec
-- The title from the share sheet wins over the page's own.
UPDATE saved_links SET
    title = CASE WHEN title = '' THEN sqlc.arg(title)::text ELSE title END,
    author = sqlc.arg(author)::text,
    thumb = sqlc.narg(thumb),
    enriched_at = now()
WHERE id = $1;
