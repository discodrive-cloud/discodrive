-- Staged uploads are real disk usage, including after a process restart.
-- No user FK: deleting an account must not erase accounting before staged files.
CREATE TABLE upload_reservations (
 id text PRIMARY KEY,
 user_id uuid NOT NULL,
 bytes bigint NOT NULL DEFAULT 0 CHECK (bytes >= 0),
 touched_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX upload_reservations_user ON upload_reservations(user_id);
CREATE INDEX upload_reservations_touched ON upload_reservations(touched_at);
