-- Indexes that repeat another index on the same columns: each one is written on every
-- insert and read by nothing the other does not serve. An index is dropped only where its
-- twin is really there (a database older than the squashed first migration may differ).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'file_versions_node_id_version_key') THEN
        DROP INDEX IF EXISTS file_versions_node_version;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'file_versions_node_size') THEN
        DROP INDEX IF EXISTS file_versions_node;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'books_node_id_key') THEN
        DROP INDEX IF EXISTS books_node;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'songs_node_id_key') THEN
        DROP INDEX IF EXISTS songs_node;
    END IF;
END
$$;
