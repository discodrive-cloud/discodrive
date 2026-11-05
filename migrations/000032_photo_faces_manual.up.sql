-- A face the user placed by hand: given to a person, or taken off one. A pass never moves
-- it, and one taken off stays out of the passes (so it does not land back where it was).
ALTER TABLE photo_faces ADD COLUMN manual boolean NOT NULL DEFAULT false;
