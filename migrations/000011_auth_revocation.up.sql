-- Device credentials belong to the authorization generation that approved them.
ALTER TABLE devices ADD COLUMN token_version bigint NOT NULL DEFAULT 0;
UPDATE devices d SET token_version = u.token_version FROM users u WHERE u.id = d.user_id;

-- Successful signed challenges may be used once across all server instances.
CREATE TABLE used_auth_challenges (
    token_hash text PRIMARY KEY,
    expires_at timestamptz NOT NULL
);
CREATE INDEX used_auth_challenges_expiry ON used_auth_challenges (expires_at);

ALTER TABLE music_settings ADD COLUMN token_version bigint NOT NULL DEFAULT 0;
UPDATE music_settings s SET token_version = u.token_version FROM users u WHERE u.id = s.user_id;
ALTER TABLE ebook_settings ADD COLUMN token_version bigint NOT NULL DEFAULT 0;
UPDATE ebook_settings s SET token_version = u.token_version FROM users u WHERE u.id = s.user_id;
