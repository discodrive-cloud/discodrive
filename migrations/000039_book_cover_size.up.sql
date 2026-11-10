-- Cached book covers live in .covers/ebooks/ and count toward the owner's quota, like
-- podcast covers. Covers cached before this migration stay at 0 until the book is reindexed.
ALTER TABLE books ADD COLUMN cover_size bigint NOT NULL DEFAULT 0;
