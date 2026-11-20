-- name: CreatePhotoUnlockKey :one
INSERT INTO photo_unlock_keys (user_id, public_key, name, token_version)
VALUES ($1, $2, $3, $4)
RETURNING id;

-- name: GetPhotoUnlockKey :one
SELECT * FROM photo_unlock_keys WHERE id = $1 AND user_id = $2;

-- name: TouchPhotoUnlockKey :exec
UPDATE photo_unlock_keys SET last_used_at = now() WHERE id = $1;

-- name: DeletePhotoUnlockKey :execrows
DELETE FROM photo_unlock_keys WHERE id = $1 AND user_id = $2;
