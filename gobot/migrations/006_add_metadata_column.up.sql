-- Migration: Add metadata column to container_logs table
-- This migration adds a JSONB column to store structured metadata for container logs

-- Add metadata column to container_logs table
ALTER TABLE container_logs
ADD COLUMN IF NOT EXISTS metadata JSONB;

-- Add a comment to document the column
COMMENT ON COLUMN container_logs.metadata IS 'JSON metadata for structured log data';
