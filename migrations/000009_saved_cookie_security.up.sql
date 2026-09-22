-- Legacy browser cookies must never remain in plaintext after upgrading.
UPDATE saved_items SET status = 'error', error_msg = 'Please submit the authenticated download again'
WHERE cookie_header IS NOT NULL AND status IN ('pending', 'processing');
UPDATE saved_items SET cookie_header = NULL WHERE cookie_header IS NOT NULL;
ALTER TABLE saved_items ADD COLUMN cookie_expires_at timestamptz;
