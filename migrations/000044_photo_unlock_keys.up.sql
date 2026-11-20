-- Device keys that open the hidden part of the gallery: a phone keeps the private half
-- behind Face ID / Touch ID and signs a server challenge instead of sending the password
-- and a TOTP code each time. token_version is the user's at registration: a password
-- change (which bumps it) turns every key off.
CREATE TABLE photo_unlock_keys (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    public_key bytea NOT NULL,
    name text NOT NULL,
    token_version bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);
CREATE INDEX photo_unlock_keys_user_idx ON photo_unlock_keys (user_id);
