DROP INDEX IF EXISTS nodes_trashed_children;
ALTER TABLE nodes DROP COLUMN IF EXISTS trash_path;
