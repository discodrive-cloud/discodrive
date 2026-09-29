-- CalDAV/CardDAV collections: sharing and integrity checks.

-- name: UpsertCollectionShare :one
-- Share a calendar or address book with a user; sharing it again with the same user
-- updates the existing share (see migration 000019).
INSERT INTO resource_shares (resource_type, resource_id, owner_id, shared_with_user, access, expires_at)
VALUES (sqlc.arg(resource_type), sqlc.arg(resource_id), sqlc.arg(owner_id), sqlc.arg(shared_with_user), sqlc.arg(access), sqlc.arg(expires_at))
ON CONFLICT (resource_type, resource_id, shared_with_user)
    WHERE shared_with_user IS NOT NULL AND resource_type IN ('calendar', 'addressbook')
DO UPDATE SET access = EXCLUDED.access, expires_at = EXCLUDED.expires_at
RETURNING *;

-- name: CreateLinkShare :one
-- A public link (feed) share, its password hash written in the same statement.
INSERT INTO resource_shares (resource_type, resource_id, owner_id, share_link_token, access, share_password_hash)
VALUES (sqlc.arg(resource_type), sqlc.arg(resource_id), sqlc.arg(owner_id), sqlc.arg(token)::text, sqlc.arg(access), sqlc.narg(password_hash))
RETURNING *;

-- name: DeleteSharesForResource :exec
DELETE FROM resource_shares WHERE resource_type = $1 AND resource_id = $2;

-- name: CalendarObjectWithUID :one
-- Another object of the collection that carries the iCalendar UID (RFC 4791 no-uid-conflict).
-- Index: calendar_objects_ical_uid (migration 000019).
SELECT uid FROM calendar_objects
WHERE calendar_id = sqlc.arg(calendar_id)
  AND parsed ->> 'uid' = sqlc.arg(ical_uid)::text
  AND uid <> sqlc.arg(object_uid)
LIMIT 1;
