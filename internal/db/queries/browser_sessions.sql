-- name: CreateBrowserSession :exec
INSERT INTO browser_sessions (id, user_id, token_version) VALUES ($1, $2, $3);

-- name: BrowserSessionActive :one
SELECT EXISTS(SELECT 1 FROM browser_sessions
 WHERE id = $1 AND user_id = $2 AND token_version = $3);

-- name: DeleteBrowserSession :exec
DELETE FROM browser_sessions WHERE id = $1 AND user_id = $2;
