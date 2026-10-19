-- name: UpsertReadingProgress :one
-- Inserts or updates the reading position for (user_id, document). All mutable
-- fields are refreshed on conflict so the latest device wins.
INSERT INTO reading_progress (user_id, document, progress, percentage, device, device_id)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id, document) DO UPDATE SET
    progress   = EXCLUDED.progress,
    percentage = EXCLUDED.percentage,
    device     = EXCLUDED.device,
    device_id  = EXCLUDED.device_id,
    updated_at = now()
RETURNING *;

-- name: GetReadingProgress :one
-- Returns the stored reading position for (user_id, document).
SELECT * FROM reading_progress WHERE user_id = $1 AND document = $2;

-- name: CountReadingProgress :one
-- Returns how many documents the user has a stored position for.
SELECT count(*) FROM reading_progress WHERE user_id = $1;
