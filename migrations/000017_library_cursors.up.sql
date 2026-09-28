-- Libraries follow the change log instead of rescanning their folders on a timer.
ALTER TABLE music_settings ADD COLUMN indexed_seq bigint NOT NULL DEFAULT 0;
ALTER TABLE ebook_settings ADD COLUMN indexed_seq bigint NOT NULL DEFAULT 0;

-- Existing libraries were indexed by the periodic scan: start them at the present, so the
-- first start does not replay each user's whole history. Startup reconciliation covers
-- anything the old scan might have missed.
UPDATE music_settings m SET indexed_seq = u.change_seq FROM users u WHERE u.id = m.user_id;
UPDATE ebook_settings e SET indexed_seq = u.change_seq FROM users u WHERE u.id = e.user_id;

-- Trashed files used to stay in the libraries until the trash was purged.
DELETE FROM songs WHERE node_id IN (SELECT id FROM nodes WHERE deleted_at IS NOT NULL);
DELETE FROM books WHERE node_id IN (SELECT id FROM nodes WHERE deleted_at IS NOT NULL);
