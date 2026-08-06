-- Widen market_data's PRIMARY KEY from (waypoint_symbol, good_symbol) to
-- (player_id, waypoint_symbol, good_symbol) — sp-hdr4p.
--
-- THE BUG. UpsertMarketData (internal/adapters/persistence/market_repository.go) deletes a
-- waypoint's rows scoped `player_id = ? AND waypoint_symbol = ?` and then inserts the fresh
-- scan. The DELETE was player-scoped; the PRIMARY KEY was not. Waypoint symbols are
-- regenerated on a universe reset and can recur, so a dead era's row could occupy the exact
-- key a live scan needed. The player-scoped DELETE could not remove it, the INSERT violated
-- the key, and the whole transaction rolled back — so that market could NEVER be cached for
-- the new era, permanently, for as long as the stale row existed.
--
-- It failed CLOSED, which is why it went unnoticed: reads return zero rows, GetMarketData
-- returns (nil, nil), and every consumer behaves exactly as if the market were unscouted. A
-- scout would re-scan it forever and never make progress. Loud in the logs, invisible in
-- behaviour.
--
-- WHY player_id AND NOT era_id. Every other era-sensitive table (gate_edges, system_coords,
-- scout_posts, the sensing tables) carries era_id, and market_data is the outlier — so era_id
-- is the intuitive answer and it is the wrong one here. What must agree is the DELETE's scope
-- and the KEY's scope, and the DELETE is scoped by player_id. So is every read of this table.
-- Adding era_id would fix the collision only indirectly, by way of era being 1:1 with player,
-- while leaving the key and the DELETE still expressed in different terms.
--
-- The sibling table settles it: shipyard_inventory, declared in the SAME source file as
-- market_data, is keyed (player_id, waypoint_symbol, ship_type) and carries era_id only as a
-- NULLABLE indexed column. A nullable column cannot participate in a primary key at all, so
-- "add era_id to the key" would additionally require backfilling a NOT NULL era for every
-- historical row through the player→era mapping — roughly 27k rows at last count, including
-- any whose player has no era row. That is a real migration risk bought for no benefit over
-- the column already sitting on the table, NOT NULL, indexed, and used by every query.
--
-- WHY THIS CANNOT FAIL ON DUPLICATE ROWS, which is what makes it safe to run unattended
-- against a populated table: the old key (waypoint_symbol, good_symbol) is a strict SUBSET of
-- the new one. Uniqueness on a subset already implies uniqueness on the superset, so no pair
-- of rows that satisfied the old constraint can violate the new one. Widening a primary key is
-- always duplicate-safe; narrowing one is not, which is the direction that needs a dry run.
--
-- DROP and ADD are ONE statement, so the table is never momentarily without a primary key.
-- Nothing references market_data — its only foreign key points outward, to players — so no
-- dependent constraint blocks the drop or is silently invalidated by it.
--
-- IDEMPOTENT, and safe in EITHER deploy order. The daemon's boot AutoMigrate performs the same
-- repair (repairMarketDataPrimaryKey in internal/infrastructure/database/connection.go),
-- because GORM's AutoMigrate adds columns and indexes but will NOT alter an existing table's
-- primary key — so a struct-tag change alone would leave production on the old key while the
-- code claimed otherwise. This file exists so the change is reviewable, revertible, and
-- appliable ahead of a deploy; the guard below makes it a no-op if boot already did it.
--
-- NO ROWS ARE DELETED HERE, deliberately. Pruning dead-era rows would cure today's instances;
-- this key change is what stops tomorrow's, and only one of those two is the fix. Pruning is
-- also not obviously wanted: TransitionEra documents an intent to RETAIN prior-era market rows
-- ("truncating would destroy the prior era's charts"), so discarding them is a separate
-- decision with a separate owner, not a tidy-up to smuggle in here.

DO $$
DECLARE
    pk_name text;
    pk_has_player boolean;
BEGIN
    SELECT c.constraint_name,
           bool_or(k.column_name = 'player_id')
      INTO pk_name, pk_has_player
      FROM information_schema.table_constraints c
      JOIN information_schema.key_column_usage k
        ON k.constraint_name = c.constraint_name
       AND k.constraint_schema = c.constraint_schema
     WHERE c.table_name = 'market_data'
       AND c.constraint_type = 'PRIMARY KEY'
     GROUP BY c.constraint_name;

    IF pk_name IS NULL THEN
        RAISE NOTICE 'market_data has no primary key (table absent?) — nothing to widen';
    ELSIF pk_has_player THEN
        RAISE NOTICE 'market_data primary key already includes player_id — no-op';
    ELSE
        EXECUTE format(
            'ALTER TABLE market_data DROP CONSTRAINT %I, ADD PRIMARY KEY (player_id, waypoint_symbol, good_symbol)',
            pk_name);
        RAISE NOTICE 'market_data primary key widened to (player_id, waypoint_symbol, good_symbol)';
    END IF;
END $$;
