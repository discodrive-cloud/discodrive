-- Deleting a node moves its bytes out of the tree, to .trash/<user>/<node id>, and
-- trash_path records where each soft-deleted row's bytes now are. Before, the bytes
-- stayed at disk_path, where a file uploaded again under the same name overwrote them.
-- NULL on a tombstone means the old layout: the bytes (if any) are still at disk_path.
-- Such tombstones are left as they are and expire through trash GC.
ALTER TABLE nodes ADD COLUMN trash_path text;

-- Restoring or purging a trashed folder walks its tombstones by parent_id, and rescan
-- lists the trashed children of every folder it visits. nodes_children covers live
-- rows only; this one covers the (small) rest.
CREATE INDEX IF NOT EXISTS nodes_trashed_children ON nodes (parent_id) WHERE deleted_at IS NOT NULL;
