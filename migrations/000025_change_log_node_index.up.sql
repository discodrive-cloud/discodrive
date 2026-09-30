-- A hard delete reads each removed node's change_log rows (their prev_path values, into
-- purged_nodes.past_paths), and the node_id cascade deletes them: without this index both
-- scanned the whole change_log per node.
-- CONCURRENTLY keeps change_log writable while it builds; it cannot run in a transaction,
-- so this file holds this one statement only. A failed build leaves an INVALID index:
-- drop it (DROP INDEX CONCURRENTLY change_log_node) before running the migration again.
CREATE INDEX CONCURRENTLY IF NOT EXISTS change_log_node ON change_log (node_id, seq);
