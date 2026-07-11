-- C1 (sp-64je): planner-visible stock. Factory-output deposits record a per-good
-- weighted-average unit cost basis so tours withdraw warehouse stock at basis
-- instead of buying our own output at laddered market asks. Basis is persisted as
-- a JSON map[good]int on the storage operation row and reloaded on recovery
-- (RULINGS #2): units are re-derived from the live ship API, but basis cannot be
-- (the API has no notion of what we paid), so it must be durable here.
--
-- Managed OUT-OF-BAND by CostBasisStore (a targeted column update); the full-row
-- operation Update omits this column so status/error writes and basis writes do
-- not clobber each other.
ALTER TABLE storage_operations
    ADD COLUMN IF NOT EXISTS cost_basis TEXT;
