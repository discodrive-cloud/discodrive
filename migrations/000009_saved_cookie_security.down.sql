UPDATE saved_items SET cookie_header = NULL;
ALTER TABLE saved_items DROP COLUMN cookie_expires_at;
