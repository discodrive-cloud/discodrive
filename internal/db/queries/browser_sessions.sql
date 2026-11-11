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

-- Everything the auth middleware needs to know about who is asking, in one read: the
-- user row, whether the browser session of the token is alive, whether its device is.
-- A session is alive while its row exists under the account's current token version and
-- it was used within the account's session lifetime (0: sessions never expire). The
-- token's own expiry cannot be the only clock: a token issued while sessions never
-- expired has none, and it would outlive any lifetime set afterwards.
-- name: AuthRequester :one
SELECT sqlc.embed(u),
       (b.id IS NOT NULL AND b.token_version = u.token_version
        AND (u.session_ttl_minutes <= 0
             OR b.last_seen > now() - make_interval(mins => u.session_ttl_minutes)))::boolean AS session_ok,
       (b.id IS NOT NULL AND b.last_seen < now() - interval '1 minute')::boolean AS session_stale,
       (d.id IS NOT NULL AND d.user_id = u.id AND d.token_hash IS NOT NULL
        AND d.token_version = u.token_version)::boolean AS device_ok
FROM users u
LEFT JOIN browser_sessions b ON b.id = sqlc.arg(session_id)::text AND b.user_id = u.id
LEFT JOIN devices d ON d.id = sqlc.narg(device_id)::uuid
WHERE u.id = sqlc.arg(user_id);

-- Notes that a session was used. At most one write a minute per session: the lifetime
-- is counted in minutes, a write on every request would buy nothing.
-- name: TouchBrowserSession :exec
UPDATE browser_sessions SET last_seen = now()
WHERE id = $1 AND last_seen < now() - interval '1 minute';

-- Sessions idle longer than their account allows are refused by AuthRequester; an hour
-- later the links they minted (stream URLs, download tickets) are over too and the row
-- is only a row.
-- name: DeleteIdleBrowserSessions :execrows
DELETE FROM browser_sessions b USING users u
WHERE u.id = b.user_id AND u.session_ttl_minutes > 0
  AND b.last_seen < now() - make_interval(mins => u.session_ttl_minutes) - interval '1 hour';
