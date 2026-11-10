-- The interval outside which an object's events cannot fall, so that a date-range listing
-- (the month view) reads only the objects that can matter instead of the whole calendar.
-- span_known = false: not computed (stored before this migration, or unreadable) — the
-- object is never filtered out; the server fills the old rows in after start.
-- A NULL bound is open (span_end of a recurring object).
ALTER TABLE calendar_objects
    ADD COLUMN span_start timestamptz,
    ADD COLUMN span_end timestamptz,
    ADD COLUMN span_known boolean NOT NULL DEFAULT false;
