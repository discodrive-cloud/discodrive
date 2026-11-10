-- name: CreateBrowserSession :exec
INSERT INTO browser_sessions (id, user_id, token_version) VALUES ($1, $2, $3);

-- name: BrowserSessionActive :one
SELECT EXISTS(SELECT 1 FROM browser_sessions
 WHERE id = $1 AND user_id = $2 AND token_version = $3);

-- name: DeleteBrowserSession :exec
DELETE FROM browser_sessions WHERE id = $1 AND user_id = $2;

-- Sessions issued under a token version the account has since left (password change,
-- "sign out everywhere"): BrowserSessionActive refuses them for good, so they are only
-- rows. Nothing else removed them but a logout from that very session.
-- name: DeleteDeadBrowserSessions :execrows
DELETE FROM browser_sessions b USING users u
WHERE u.id = b.user_id AND b.token_version <> u.token_version;
