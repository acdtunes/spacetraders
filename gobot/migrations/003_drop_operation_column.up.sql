-- Migration: Drop operation column from ship_assignments table
-- The operation column is redundant as container logs already track what operations are running

ALTER TABLE ship_assignments DROP COLUMN IF EXISTS operation;
