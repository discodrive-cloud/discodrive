-- GetLiveNodeByPath (every WebDAV entry, sync by path) looked nodes up by disk_path
-- with a sequential scan of the whole table.
CREATE INDEX IF NOT EXISTS nodes_live_path ON nodes (user_id, disk_path) WHERE deleted_at IS NULL;
