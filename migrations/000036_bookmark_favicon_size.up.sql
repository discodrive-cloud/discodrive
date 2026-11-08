-- Favicons of bookmarks are stored files and count against the owner's quota.
ALTER TABLE browser_bookmarks ADD COLUMN favicon_size bigint NOT NULL DEFAULT 0;
