ALTER TABLE calendar_objects
    DROP COLUMN IF EXISTS span_known,
    DROP COLUMN IF EXISTS span_end,
    DROP COLUMN IF EXISTS span_start;
