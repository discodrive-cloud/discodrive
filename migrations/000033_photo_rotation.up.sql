-- The clockwise turn (0, 90, 180, 270) the user gave a picture in the gallery, applied on
-- top of the file's own orientation when thumbnails and previews are made. The file stays as it is.
ALTER TABLE photo_assets ADD COLUMN rotation smallint NOT NULL DEFAULT 0 CHECK (rotation IN (0, 90, 180, 270));
