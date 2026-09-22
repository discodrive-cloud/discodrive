-- name: GetEbookSettings :one
SELECT * FROM ebook_settings WHERE user_id = $1;

-- name: UpsertEbookSettings :one
INSERT INTO ebook_settings (user_id, enabled, folder_node_id, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (user_id) DO UPDATE SET enabled = EXCLUDED.enabled,
    folder_node_id = EXCLUDED.folder_node_id, updated_at = now()
RETURNING *;

-- name: SetEbookCredentials :exec
UPDATE ebook_settings SET password_cipher = $2, api_key = $3, token_version = $4, updated_at = now()
WHERE user_id = $1;

-- name: ClearEbookCredentials :exec
UPDATE ebook_settings SET password_cipher = NULL, api_key = NULL, updated_at = now()
WHERE user_id = $1;

-- name: GetEbookSettingsByApiKey :one
SELECT s.* FROM ebook_settings s JOIN users u ON u.id = s.user_id
WHERE s.api_key = $1 AND s.token_version = u.token_version AND NOT u.must_change_password;

-- name: EnabledEbookUsers :many
SELECT user_id, folder_node_id FROM ebook_settings WHERE enabled = true AND folder_node_id IS NOT NULL;

-- name: UpsertBook :one
INSERT INTO books (user_id, node_id, title, sort_title, language, isbn, description, publisher,
    published_date, series, series_index, format, content_type, size)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (node_id) DO UPDATE SET
    user_id        = EXCLUDED.user_id,
    title          = EXCLUDED.title,
    sort_title     = EXCLUDED.sort_title,
    language       = EXCLUDED.language,
    isbn           = EXCLUDED.isbn,
    description    = EXCLUDED.description,
    publisher      = EXCLUDED.publisher,
    published_date = EXCLUDED.published_date,
    series         = EXCLUDED.series,
    series_index   = EXCLUDED.series_index,
    format         = EXCLUDED.format,
    content_type   = EXCLUDED.content_type,
    size           = EXCLUDED.size,
    updated_at     = now()
RETURNING *;

-- name: DeleteBookByNode :exec
DELETE FROM books WHERE node_id = $1;

-- name: GetBookByNode :one
SELECT * FROM books WHERE node_id = $1;

-- name: SetBookCoverPath :exec
UPDATE books SET cover_path = $2 WHERE id = $1;

-- name: InsertBookAuthor :exec
INSERT INTO book_authors (book_id, name, sort_name) VALUES ($1, $2, $3)
ON CONFLICT (book_id, name) DO UPDATE SET sort_name = EXCLUDED.sort_name;

-- name: ClearBookAuthors :exec
DELETE FROM book_authors WHERE book_id = $1;

-- name: InsertBookTag :exec
INSERT INTO book_tags (book_id, tag) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ClearBookTags :exec
DELETE FROM book_tags WHERE book_id = $1;

-- name: BookAuthors :many
SELECT name, sort_name FROM book_authors WHERE book_id = $1 ORDER BY sort_name, name;

-- name: BookTags :many
SELECT tag FROM book_tags WHERE book_id = $1 ORDER BY tag;

-- name: AccessibleBook :one
-- Returns a single book by id if it is accessible to the user (own or shared).
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT b.* FROM books b
WHERE b.id = $2
  AND (b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree))
LIMIT 1;

-- name: AccessibleBooksAll :many
-- Returns all books accessible to a user, ordered by sort_title, with pagination.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT b.* FROM books b
WHERE b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree)
ORDER BY b.sort_title, b.title
LIMIT $2 OFFSET $3;

-- name: CountAccessibleBooks :one
-- Returns the total count of books accessible to a user.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT COUNT(*) FROM books b
WHERE b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree);

-- name: AccessibleBooksNewest :many
-- Returns recently added books accessible to a user, newest first.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT b.* FROM books b
WHERE b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree)
ORDER BY b.added_at DESC
LIMIT $2 OFFSET $3;

-- name: AccessibleBooksByAuthor :many
-- Returns books accessible to a user filtered by author name.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT b.* FROM books b
JOIN book_authors ba ON ba.book_id = b.id
WHERE ba.name = $2
  AND (b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree))
ORDER BY b.sort_title, b.title
LIMIT $3 OFFSET $4;

-- name: AccessibleBooksBySeries :many
-- Returns books accessible to a user for a given series, ordered by series_index.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT b.* FROM books b
WHERE b.series = $2
  AND (b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree))
ORDER BY b.series_index, b.sort_title
LIMIT $3 OFFSET $4;

-- name: AccessibleBooksByGenre :many
-- Returns books accessible to a user filtered by genre/tag.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT b.* FROM books b
JOIN book_tags bt ON bt.book_id = b.id
WHERE bt.tag = $2
  AND (b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree))
ORDER BY b.sort_title, b.title
LIMIT $3 OFFSET $4;

-- name: AccessibleAuthors :many
-- Returns distinct authors with their sort_name for navigation, from accessible books.
-- Both name and sort_name are selected so ORDER BY sort_name satisfies DISTINCT rules.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT ba.name, ba.sort_name FROM book_authors ba
JOIN books b ON b.id = ba.book_id
WHERE b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree)
ORDER BY ba.sort_name, ba.name;

-- name: AccessibleSeries :many
-- Returns distinct series names from accessible books, ordered alphabetically.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT b.series FROM books b
WHERE b.series IS NOT NULL AND b.series != ''
  AND (b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree))
ORDER BY b.series;

-- name: AccessibleBookGenres :many
-- Returns distinct genre/tag values from accessible books, ordered alphabetically.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT bt.tag FROM book_tags bt
JOIN books b ON b.id = bt.book_id
WHERE b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree)
ORDER BY bt.tag;

-- name: SearchAccessibleBooks :many
-- Returns accessible books matching a case-insensitive substring in title, author, or series.
WITH RECURSIVE shared_subtree AS (
    SELECT resource_id AS node_id FROM resource_shares
    WHERE resource_type = 'file_node' AND shared_with_user = $1
      AND (expires_at IS NULL OR expires_at > now())
    UNION
    SELECT n.id FROM nodes n JOIN shared_subtree st ON n.parent_id = st.node_id
)
SELECT DISTINCT b.* FROM books b
LEFT JOIN book_authors ba ON ba.book_id = b.id
WHERE (b.user_id = $1 OR b.node_id IN (SELECT node_id FROM shared_subtree))
  AND (
    b.title  ILIKE '%' || $2 || '%' OR
    ba.name  ILIKE '%' || $2 || '%' OR
    b.series ILIKE '%' || $2 || '%'
  )
ORDER BY b.sort_title, b.title
LIMIT $3 OFFSET $4;

-- name: UpdateBookMetadata :exec
UPDATE books SET
    title = $3,
    sort_title = $4,
    language = $5,
    description = $6,
    publisher = $7,
    published_date = $8,
    series = $9,
    series_index = $10,
    metadata_edited = true,
    updated_at = now()
WHERE id = $1 AND user_id = $2;

-- name: SetBookMetadataEdited :exec
UPDATE books SET metadata_edited = $2, updated_at = now()
WHERE id = $1;
