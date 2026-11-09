DROP TABLE photo_dup_dismissed;
DROP INDEX photo_assets_phash_backfill_idx;
ALTER TABLE photo_assets DROP COLUMN phash_flat;
ALTER TABLE photo_assets DROP COLUMN phash;
