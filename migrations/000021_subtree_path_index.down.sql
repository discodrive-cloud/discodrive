DROP INDEX IF EXISTS nodes_trashed_path;
DROP INDEX IF EXISTS nodes_live_path;
CREATE INDEX nodes_live_path ON nodes (user_id, disk_path) WHERE deleted_at IS NULL;
