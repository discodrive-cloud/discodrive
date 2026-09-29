-- Renaming, moving or deleting a folder matched its subtree with
-- starts_with(disk_path, prefix || '/'), which no btree index serves under the database's
-- en_US collation: every such operation read the whole nodes table. text_pattern_ops
-- compares bytes, so the same prefix becomes an index range,
--   disk_path ~>=~ prefix || '/' AND disk_path ~<~ prefix || '0'   ('0' follows '/'),
-- which is what the subtree queries now say. Equality (GetLiveNodeByPath) is part of the
-- same operator class, so the live-path index is rebuilt with it instead of adding a
-- second index over the same rows. Tombstones get their own small partial index: a
-- folder rename also rewrites the paths of trashed rows under it.
DROP INDEX IF EXISTS nodes_live_path;
CREATE INDEX nodes_live_path ON nodes (user_id, disk_path text_pattern_ops) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS nodes_trashed_path ON nodes (user_id, disk_path text_pattern_ops) WHERE deleted_at IS NOT NULL;
