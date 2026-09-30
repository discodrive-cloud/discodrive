-- Removing a node for good (purge from the trash, trash retention) deletes its
-- change_log rows with it (node_id cascades), including the soft delete. A client whose
-- cursor predates that delete then never learned the node was gone and kept it forever.
-- Each hard-deleted node leaves a row here under a fresh seq from users.change_seq, and
-- the sync feed merges these rows in as deletes. No reference to nodes on purpose: the
-- row must outlive the node.
-- Kept forever: a client may resume from any cursor, however old, and must still learn
-- about every node removed since. The rows are small (one per purged node).
-- Every cursor gets every row, 0 included: a client learns node ids from its own
-- uploads too, before its first pull.
-- past_paths: the node's earlier paths (the prev_path of its moves), read before its
-- change_log rows cascade away. The scoped feed matches them too, so a node moved out of
-- a sync folder and then purged still reaches the clients of that folder.
CREATE TABLE purged_nodes (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    seq        bigint NOT NULL,
    node_id    uuid NOT NULL,
    name       text NOT NULL,
    parent_id  uuid,
    is_dir     boolean NOT NULL,
    disk_path  text,
    version    bigint NOT NULL,
    past_paths text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, seq)
);

-- Wake SSE subscribers and the library tracker the same way a change_log row does.
CREATE TRIGGER purged_nodes_notify AFTER INSERT ON purged_nodes
    FOR EACH ROW EXECUTE FUNCTION notify_change_log();
