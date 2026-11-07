-- Links shared from phones ("Share → DiscoDrive"): an inbox the user sorts through on a
-- computer. Only the link and what helps recognise it are stored; the thumbnail is a small
-- re-encoded JPEG made by the server (never the upstream bytes) and counts toward quota.
CREATE TABLE saved_links (
    id uuid DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    url text NOT NULL,
    source text DEFAULT 'other'::text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    author text DEFAULT ''::text NOT NULL,
    thumb bytea,
    enriched_at timestamp with time zone,
    archived_at timestamp with time zone,
    saved_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT saved_links_user_url_key UNIQUE (user_id, url)
);

CREATE INDEX saved_links_user_saved ON saved_links USING btree (user_id, saved_at DESC);
CREATE INDEX saved_links_needs_enrich ON saved_links USING btree (saved_at) WHERE enriched_at IS NULL;
