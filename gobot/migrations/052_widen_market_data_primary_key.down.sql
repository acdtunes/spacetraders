-- Rollback for 052: narrow market_data's PRIMARY KEY back to (waypoint_symbol, good_symbol).
--
-- READ THIS BEFORE RUNNING IT. The up-migration is duplicate-safe because widening a key can
-- never turn a legal row pair into an illegal one. This direction is the opposite: NARROWING a
-- key can fail, and here it is actually LIKELY to. The whole point of the widening is that two
-- players may legitimately hold rows at the same (waypoint_symbol, good_symbol) — that is the
-- recurring-waypoint case sp-hdr4p is about. Once any such pair exists, this rollback cannot
-- run without discarding one of them.
--
-- It therefore refuses rather than guessing which row to destroy. If it raises, the schema is
-- left exactly as it was; decide explicitly whose rows to drop and re-run.
--
-- Note also that the daemon's boot AutoMigrate re-applies the widening
-- (repairMarketDataPrimaryKey), so a rollback that is not accompanied by rolling the daemon
-- back will be undone on its next start. That is intentional — the widened key is the correct
-- schema — but it means this file is only meaningful as part of a coordinated revert.

DO $$
DECLARE
    pk_name text;
    dupes bigint;
BEGIN
    SELECT c.constraint_name
      INTO pk_name
      FROM information_schema.table_constraints c
     WHERE c.table_name = 'market_data'
       AND c.constraint_type = 'PRIMARY KEY';

    IF pk_name IS NULL THEN
        RAISE NOTICE 'market_data has no primary key — nothing to narrow';
        RETURN;
    END IF;

    SELECT count(*)
      INTO dupes
      FROM (
          SELECT 1
            FROM market_data
           GROUP BY waypoint_symbol, good_symbol
          HAVING count(*) > 1
      ) d;

    IF dupes > 0 THEN
        RAISE EXCEPTION
            'refusing to narrow market_data primary key: % (waypoint_symbol, good_symbol) pairs are held by more than one player. Narrowing would silently destroy one row per pair. Decide which players to prune, delete them explicitly, then re-run.',
            dupes;
    END IF;

    EXECUTE format(
        'ALTER TABLE market_data DROP CONSTRAINT %I, ADD PRIMARY KEY (waypoint_symbol, good_symbol)',
        pk_name);
    RAISE NOTICE 'market_data primary key narrowed to (waypoint_symbol, good_symbol)';
END $$;
