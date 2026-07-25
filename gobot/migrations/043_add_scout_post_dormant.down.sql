-- Rollback the dormant column from scout_posts.

ALTER TABLE scout_posts
    DROP COLUMN IF EXISTS dormant;
