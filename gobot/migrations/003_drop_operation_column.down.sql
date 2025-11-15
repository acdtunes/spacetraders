-- Rollback: Add operation column back to ship_assignments table

ALTER TABLE ship_assignments ADD COLUMN IF NOT EXISTS operation TEXT;
