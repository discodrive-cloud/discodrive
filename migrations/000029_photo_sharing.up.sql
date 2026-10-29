-- Gallery sharing (stage 3): a partner sees the owner's whole gallery, albums gather cards
-- of anyone their members can see, and an album can be opened to people without an
-- account through a link.

-- owner → viewer. in_feed is the viewer's choice: mix the owner's photos into their feed.
CREATE TABLE photo_partners (
    owner_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    viewer_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    in_feed    boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, viewer_id),
    CHECK (owner_id <> viewer_id)
);
CREATE INDEX photo_partners_viewer ON photo_partners (viewer_id);

CREATE TABLE photo_albums (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX photo_albums_owner ON photo_albums (owner_id);

CREATE TABLE photo_album_members (
    album_id   uuid NOT NULL REFERENCES photo_albums(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (album_id, user_id)
);
CREATE INDEX photo_album_members_user ON photo_album_members (user_id);

-- A card stays in its albums while hidden or in the trash (it is just not shown); purging
-- the file removes the row with the asset.
CREATE TABLE photo_album_items (
    album_id uuid NOT NULL REFERENCES photo_albums(id) ON DELETE CASCADE,
    asset_id uuid NOT NULL REFERENCES photo_assets(id) ON DELETE CASCADE,
    added_by uuid REFERENCES users(id) ON DELETE SET NULL,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (album_id, asset_id)
);
CREATE INDEX photo_album_items_asset ON photo_album_items (asset_id);

CREATE TABLE photo_album_links (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    album_id       uuid NOT NULL REFERENCES photo_albums(id) ON DELETE CASCADE,
    token          text NOT NULL UNIQUE,
    password_hash  text,
    expires_at     timestamptz,
    allow_download boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX photo_album_links_album ON photo_album_links (album_id);
