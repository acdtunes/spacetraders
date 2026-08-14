-- Rollback the scan-dedup allowlist table (sp-7q61f). Idempotent via IF EXISTS.

DROP TABLE IF EXISTS scan_dedup_allowlist;
