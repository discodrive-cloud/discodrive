-- Legacy pending enrollments have no proof of identity and must be restarted.
ALTER TABLE user_totp ADD COLUMN approval_id text NOT NULL DEFAULT '';
DELETE FROM user_totp WHERE NOT enabled;
