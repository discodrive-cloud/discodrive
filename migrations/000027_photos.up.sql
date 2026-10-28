-- Photos gallery. Server-wide map settings live in the existing settings table under the
-- keys photos.maps.region and photos.maps.state (no rows are seeded: absent = defaults).
CREATE TABLE photo_settings (
    user_id     uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    enabled     boolean NOT NULL DEFAULT false,
    indexed_seq bigint NOT NULL DEFAULT 0,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE photo_sources (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, folder_node_id)
);

CREATE TABLE photo_assets (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id         uuid NOT NULL UNIQUE REFERENCES nodes(id) ON DELETE CASCADE,
    kind            text NOT NULL,
    taken_at        timestamptz NOT NULL,
    -- Wall-clock time at the place of shooting. Deliberately timestamp, not timestamptz:
    -- month grouping and the on-this-day index must not depend on the session time zone.
    taken_local     timestamp NOT NULL,
    tz_offset_min   int,
    date_source     text NOT NULL DEFAULT 'file' CHECK (date_source IN ('exif', 'xmp', 'file', 'edit')),
    lat             double precision,
    lon             double precision,
    -- GeoNames id of the nearest place, named in the reader's language at read time;
    -- 0 = looked up, nothing near. NULL = not looked up yet (no geocoder then).
    place_id        integer,
    country_code    text,
    -- The user erased the position of a file that keeps its own (video, RAW: only a sidecar
    -- can be written); a re-read must not bring it back until the content changes.
    place_cleared   boolean NOT NULL DEFAULT false,
    make            text,
    model           text,
    lens            text,
    f_number        real,
    focal_mm        real,
    exposure        text,
    iso             int,
    width           int,
    height          int,
    duration_ms     bigint,
    video_codec     text,
    is_hdr          boolean NOT NULL DEFAULT false,
    live_cid        text,
    content_hash    text,
    parent_id       uuid REFERENCES photo_assets(id) ON DELETE SET NULL,
    role            text CHECK (role IN ('live-video', 'original')),
    favorite        boolean NOT NULL DEFAULT false,
    hidden          boolean NOT NULL DEFAULT false,
    trashed_at      timestamptz,
    thumb_state     text NOT NULL DEFAULT 'pending' CHECK (thumb_state IN ('pending', 'ready', 'failed')),
    thumb_error     text,
    -- The metadata read failed (the file is still shown, dated from the file); listed in the
    -- settings' failed-files list until a re-read succeeds.
    index_error     text,
    transcode_state text,
    indexed_at      timestamptz,
    node_version    bigint
);

-- Month lists, buckets and the hidden list range over taken_local; no query ranges over
-- taken_at, so this replaces the taken_at timeline index the brief listed.
CREATE INDEX photo_assets_timeline ON photo_assets (user_id, taken_local DESC)
    WHERE parent_id IS NULL AND trashed_at IS NULL;
CREATE INDEX photo_assets_live_cid ON photo_assets (user_id, live_cid);
CREATE INDEX photo_assets_content_hash ON photo_assets (user_id, content_hash);
CREATE INDEX photo_assets_on_this_day ON photo_assets
    (user_id, (extract(month from taken_local)), (extract(day from taken_local)));
-- ClaimPendingThumbs walks pending rows in claim order; without the order in the index
-- every claim would sort all pending rows (90k on the first run).
CREATE INDEX photo_assets_thumb_pending ON photo_assets (indexed_at NULLS FIRST, id) WHERE thumb_state = 'pending';
CREATE INDEX photo_assets_parent ON photo_assets (parent_id) WHERE parent_id IS NOT NULL;
