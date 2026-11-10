-- What a user's files and their versions occupy, kept as a running count. The quota check
-- asks for it on every write; summing the file tree each time made a first sync of tens
-- of thousands of files slower with every file it added.
--
-- Every change to a file's size adds a row with the difference, in the transaction that
-- makes the change, so the count is exactly as committed as the files are. Rows are only
-- ever inserted here — writers never wait for one another on a shared counter — and the
-- server folds them into one row a user in the background. A user occupies SUM(bytes).
-- No foreign key: a deleted account's files subtract themselves on the way out.
CREATE TABLE file_usage_deltas (
    user_id uuid NOT NULL,
    bytes bigint NOT NULL
);
CREATE INDEX file_usage_deltas_user ON file_usage_deltas (user_id);

CREATE FUNCTION add_file_usage(owner uuid, delta bigint) RETURNS void
    LANGUAGE sql AS $$
    INSERT INTO file_usage_deltas (user_id, bytes) SELECT owner, delta WHERE owner IS NOT NULL AND delta <> 0;
$$;

CREATE FUNCTION file_usage_node_added() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    PERFORM add_file_usage(NEW.user_id, NEW.size);
    RETURN NULL;
END;
$$;

-- A size change is the difference. A change of owner moves the file and its versions.
CREATE FUNCTION file_usage_node_changed() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    was bigint := CASE WHEN OLD.is_dir THEN 0 ELSE COALESCE(OLD.size, 0) END;
    cur bigint := CASE WHEN NEW.is_dir THEN 0 ELSE COALESCE(NEW.size, 0) END;
    history bigint;
BEGIN
    IF OLD.user_id = NEW.user_id THEN
        PERFORM add_file_usage(NEW.user_id, cur - was);
        RETURN NULL;
    END IF;
    SELECT COALESCE(SUM(size), 0) INTO history FROM file_versions WHERE node_id = NEW.id;
    PERFORM add_file_usage(OLD.user_id, -(was + history));
    PERFORM add_file_usage(NEW.user_id, cur + history);
    RETURN NULL;
END;
$$;

-- A file goes with its versions. They are counted here, before the delete: by the time
-- the cascade removes them the file is gone and nothing says whose they were.
CREATE FUNCTION file_usage_node_removed() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    was bigint := CASE WHEN OLD.is_dir THEN 0 ELSE COALESCE(OLD.size, 0) END;
    history bigint;
BEGIN
    SELECT COALESCE(SUM(size), 0) INTO history FROM file_versions WHERE node_id = OLD.id;
    PERFORM add_file_usage(OLD.user_id, -(was + history));
    RETURN OLD;
END;
$$;

-- A version belongs to whoever owns its file. A version removed by the cascade of its
-- file finds no owner and adds nothing: file_usage_node_removed has counted it.
CREATE FUNCTION file_usage_version() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM add_file_usage((SELECT user_id FROM nodes WHERE id = OLD.node_id), -COALESCE(OLD.size, 0));
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM add_file_usage((SELECT user_id FROM nodes WHERE id = NEW.node_id), COALESCE(NEW.size, 0));
    END IF;
    RETURN NULL;
END;
$$;

-- The conditions keep the bookkeeping off writes that do not change what is occupied:
-- a rename, a move, a trip to the trash and back.
CREATE TRIGGER file_usage_added AFTER INSERT ON nodes
    FOR EACH ROW WHEN (NOT NEW.is_dir AND COALESCE(NEW.size, 0) <> 0)
    EXECUTE FUNCTION file_usage_node_added();
CREATE TRIGGER file_usage_changed AFTER UPDATE ON nodes
    FOR EACH ROW WHEN (OLD.size IS DISTINCT FROM NEW.size OR OLD.is_dir <> NEW.is_dir OR OLD.user_id <> NEW.user_id)
    EXECUTE FUNCTION file_usage_node_changed();
CREATE TRIGGER file_usage_removed BEFORE DELETE ON nodes
    FOR EACH ROW EXECUTE FUNCTION file_usage_node_removed();

CREATE TRIGGER file_usage_version_added AFTER INSERT ON file_versions
    FOR EACH ROW WHEN (COALESCE(NEW.size, 0) <> 0)
    EXECUTE FUNCTION file_usage_version();
CREATE TRIGGER file_usage_version_changed AFTER UPDATE ON file_versions
    FOR EACH ROW WHEN (OLD.size IS DISTINCT FROM NEW.size OR OLD.node_id <> NEW.node_id)
    EXECUTE FUNCTION file_usage_version();
CREATE TRIGGER file_usage_version_removed AFTER DELETE ON file_versions
    FOR EACH ROW WHEN (COALESCE(OLD.size, 0) <> 0)
    EXECUTE FUNCTION file_usage_version();

-- The triggers above hold a lock that keeps writers out until this commits, so the first
-- count and the files it counts are one moment.
INSERT INTO file_usage_deltas (user_id, bytes)
SELECT owned.user_id, SUM(owned.bytes)::bigint
FROM (
    SELECT user_id, COALESCE(size, 0) AS bytes FROM nodes WHERE NOT is_dir
    UNION ALL
    SELECT n.user_id, COALESCE(v.size, 0) FROM file_versions v JOIN nodes n ON n.id = v.node_id
) AS owned
GROUP BY owned.user_id
HAVING SUM(owned.bytes) <> 0;
