-- Perceptual hash (dHash) of the thumbnail; NULL = not computed yet. A uint64 is stored
-- as bigint by bit cast. phash_flat: the picture has (almost) no contrast — a black frame,
-- a solid colour — so its hash says nothing and the card is never a duplicate candidate.
ALTER TABLE photo_assets ADD COLUMN phash bigint;
ALTER TABLE photo_assets ADD COLUMN phash_flat boolean NOT NULL DEFAULT false;

-- The hash backfill: top-level cards with a thumbnail and no hash yet.
CREATE INDEX photo_assets_phash_backfill_idx ON photo_assets (id)
    WHERE phash IS NULL AND thumb_state = 'ready' AND parent_id IS NULL;

-- Pairs of cards the user said are not duplicates («Оставить все»). A card is named by its
-- content (content_hash, or the asset id as text when the hash is unknown), so a byte copy
-- that becomes the card's representative later keeps the decision. a < b byte-wise (C
-- collation, as Go compares strings), so a pair is stored once.
CREATE TABLE photo_dup_dismissed (
    user_id    uuid NOT NULL REFERENCES users ON DELETE CASCADE,
    a          text NOT NULL,
    b          text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, a, b),
    CHECK (a COLLATE "C" < b COLLATE "C")
);
