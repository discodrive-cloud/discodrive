-- name: GetBootstrap :one
SELECT * FROM server_bootstrap WHERE singleton = true;

-- name: LockBootstrap :one
SELECT * FROM server_bootstrap WHERE singleton = true FOR UPDATE;

-- name: SetBootstrapTokenHash :exec
UPDATE server_bootstrap SET token_hash = $1 WHERE singleton = true AND completed = false;

-- name: CompleteBootstrap :exec
UPDATE server_bootstrap SET completed = true, token_hash = '' WHERE singleton = true;
