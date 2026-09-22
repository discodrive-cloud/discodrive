-- name: ConsumeAuthChallenge :execrows
INSERT INTO used_auth_challenges (token_hash, expires_at)
SELECT sqlc.arg(token_hash)::text, sqlc.arg(expires_at)::timestamptz
WHERE sqlc.arg(expires_at)::timestamptz > clock_timestamp()
ON CONFLICT (token_hash) DO NOTHING;

-- name: DeleteExpiredAuthChallenges :exec
DELETE FROM used_auth_challenges WHERE expires_at < now();
