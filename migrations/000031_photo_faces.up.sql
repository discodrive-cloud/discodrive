-- Photos gallery: people. Faces are found by an external ML service (PHOTOS_ML_URL) and
-- grouped into persons by the server; every person belongs to one user.
ALTER TABLE photo_settings ADD COLUMN faces_enabled boolean NOT NULL DEFAULT false;

-- NULL = not looked at yet (or the content changed since); 'failed' = the file could not be
-- rendered (the ML service being down never fails a file).
ALTER TABLE photo_assets ADD COLUMN face_state text CHECK (face_state IN ('done', 'failed'));
ALTER TABLE photo_assets ADD COLUMN face_error text;
-- the face model the card was looked at with: vectors of two models do not compare
ALTER TABLE photo_assets ADD COLUMN face_model text;
-- ClaimPendingFaces walks cards newest first; the order must be in the index (90k rows).
CREATE INDEX photo_assets_face_pending ON photo_assets (taken_local DESC, id)
    WHERE face_state IS NULL AND parent_id IS NULL AND thumb_state = 'ready';

CREATE TABLE photo_persons (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          text NOT NULL DEFAULT '',
    hidden        boolean NOT NULL DEFAULT false,
    birth_date    date, -- the user's entry: ages on photos
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX photo_persons_user ON photo_persons (user_id);

CREATE TABLE photo_faces (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    asset_id   uuid NOT NULL REFERENCES photo_assets(id) ON DELETE CASCADE,
    person_id  uuid REFERENCES photo_persons(id) ON DELETE SET NULL,
    -- the box as fractions (0..1) of the picture the service was given (the viewer preview)
    x1 real NOT NULL, y1 real NOT NULL, x2 real NOT NULL, y2 real NOT NULL,
    score      real NOT NULL,
    -- 512 float32 little endian, normalised (cosine distance = 1 - dot product)
    embedding  bytea NOT NULL,
    -- the model that made the embedding: vectors of two models do not compare
    model      text NOT NULL,
    -- seen by a clustering pass: later passes look again only at faces near new ones
    clustered  boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX photo_faces_asset ON photo_faces (asset_id);
CREATE INDEX photo_faces_person ON photo_faces (person_id) WHERE person_id IS NOT NULL;
CREATE INDEX photo_faces_unassigned ON photo_faces (user_id) WHERE person_id IS NULL;
