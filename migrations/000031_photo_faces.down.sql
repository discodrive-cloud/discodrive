DROP TABLE IF EXISTS photo_faces;
DROP TABLE IF EXISTS photo_persons;
DROP INDEX IF EXISTS photo_assets_face_pending;
ALTER TABLE photo_assets DROP COLUMN IF EXISTS face_model;
ALTER TABLE photo_assets DROP COLUMN IF EXISTS face_error;
ALTER TABLE photo_assets DROP COLUMN IF EXISTS face_state;
ALTER TABLE photo_settings DROP COLUMN IF EXISTS faces_enabled;
