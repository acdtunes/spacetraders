-- Rollback the hot_waypoints column from scout_posts.

ALTER TABLE scout_posts
    DROP COLUMN IF EXISTS hot_waypoints;
