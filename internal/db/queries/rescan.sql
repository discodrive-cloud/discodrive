-- name: CreateRescanRequest :one
INSERT INTO rescan_requests (user_id, requested_by) VALUES ($1, $2) RETURNING *;

-- name: ListPendingRescanRequests :many
SELECT * FROM rescan_requests WHERE finished_at IS NULL ORDER BY id;

-- name: StartRescanRequest :exec
UPDATE rescan_requests SET started_at = now() WHERE id = $1;

-- name: FinishRescanRequest :exec
UPDATE rescan_requests
SET finished_at = now(), imported = $2, missing = $3, changed = $4, errors = $5, error_text = $6
WHERE id = $1;

-- name: GetRescanRequest :one
SELECT * FROM rescan_requests WHERE id = $1;

-- name: ListRecentRescanRequests :many
SELECT r.*, u.email
FROM rescan_requests r LEFT JOIN users u ON u.id = r.user_id
ORDER BY r.id DESC LIMIT $1;
