-- Setup completion is independent of the user list: deleting the last admin must
-- never reopen public onboarding. Existing installations start closed.
CREATE TABLE server_bootstrap (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    completed boolean NOT NULL DEFAULT false,
    token_hash text NOT NULL DEFAULT ''
);
INSERT INTO server_bootstrap (singleton, completed)
VALUES (true, EXISTS (SELECT 1 FROM users) OR EXISTS (SELECT 1 FROM tenants));
-- Remove secrets left by older bootstrap implementations.
DELETE FROM settings WHERE key = 'admin.setup_token';
