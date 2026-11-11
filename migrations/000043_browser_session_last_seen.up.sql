-- When a browser session was last used. A session token may carry no expiry of its own
-- (issued while the account's sessions never expired); the row is then the only place
-- that can say the session has been idle longer than the account now allows.
ALTER TABLE browser_sessions ADD COLUMN last_seen timestamptz NOT NULL DEFAULT now();
