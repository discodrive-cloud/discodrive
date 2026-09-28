-- Disk↔database reconciliation runs on demand only: at startup, from the admin panel and
-- via `server rescan`. Requests queue here; the running server is the only executor.
CREATE TABLE rescan_requests (
    id           bigserial PRIMARY KEY,
    user_id      uuid REFERENCES users(id) ON DELETE CASCADE, -- NULL = every user
    requested_by text NOT NULL,                               -- admin:<uuid> | cli | startup
    created_at   timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz,
    imported     integer NOT NULL DEFAULT 0,
    missing      integer NOT NULL DEFAULT 0,
    changed      integer NOT NULL DEFAULT 0,
    errors       integer NOT NULL DEFAULT 0,
    error_text   text
);
CREATE INDEX rescan_requests_pending ON rescan_requests (id) WHERE finished_at IS NULL;

CREATE FUNCTION notify_rescan_request() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('rescan_requests', NEW.id::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER rescan_requests_notify AFTER INSERT ON rescan_requests
    FOR EACH ROW EXECUTE FUNCTION notify_rescan_request();
