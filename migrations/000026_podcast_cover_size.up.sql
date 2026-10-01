ALTER TABLE podcast_channels ADD COLUMN cover_size bigint NOT NULL DEFAULT 0 CHECK (cover_size >= 0);
