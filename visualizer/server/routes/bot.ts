import { Router } from 'express';
import Database from 'better-sqlite3';
import path from 'path';

const router = Router();

// Path to bot's SQLite database
const BOT_DB_PATH = path.resolve(process.cwd(), '../../bot/var/data/sqlite/spacetraders.db');

// Helper to get database connection
function getDatabase() {
  return new Database(BOT_DB_PATH, { readonly: true, fileMustExist: true });
}

// Get all ship assignments
router.get('/assignments', async (req, res) => {
  try {
    const db = getDatabase();
    const assignments = db.prepare(`
      SELECT
        ship_symbol,
        player_id,
        assigned_to,
        daemon_id,
        operation,
        status,
        assigned_at,
        released_at,
        metadata
      FROM ship_assignments
      WHERE status = 'active'
    `).all();

    db.close();

    // Parse metadata JSON
    const parsed = assignments.map((a: any) => ({
      ...a,
      metadata: a.metadata ? JSON.parse(a.metadata) : null,
    }));

    res.json({ assignments: parsed });
  } catch (error) {
    console.error('Failed to fetch assignments:', error);
    res.status(500).json({ error: 'Failed to fetch assignments' });
  }
});

// Get assignment for specific ship
router.get('/assignments/:shipSymbol', async (req, res) => {
  try {
    const db = getDatabase();
    const assignment = db.prepare(`
      SELECT
        ship_symbol,
        player_id,
        assigned_to,
        daemon_id,
        operation,
        status,
        assigned_at,
        released_at,
        metadata
      FROM ship_assignments
      WHERE ship_symbol = ?
    `).get(req.params.shipSymbol);

    db.close();

    if (!assignment) {
      return res.status(404).json({ error: 'Assignment not found' });
    }

    const parsed = {
      ...assignment,
      metadata: (assignment as any).metadata ? JSON.parse((assignment as any).metadata) : null,
    };

    res.json({ assignment: parsed });
  } catch (error) {
    console.error('Failed to fetch assignment:', error);
    res.status(500).json({ error: 'Failed to fetch assignment' });
  }
});

// Get all active daemons
router.get('/daemons', async (req, res) => {
  try {
    const db = getDatabase();
    const daemons = db.prepare(`
      SELECT
        daemon_id,
        player_id,
        pid,
        command,
        started_at,
        stopped_at,
        status,
        log_file,
        err_file
      FROM daemons
      WHERE status IN ('running', 'stopping')
    `).all();

    db.close();

    // Parse command JSON
    const parsed = daemons.map((d: any) => ({
      ...d,
      command: d.command ? JSON.parse(d.command) : null,
    }));

    res.json({ daemons: parsed });
  } catch (error) {
    console.error('Failed to fetch daemons:', error);
    res.status(500).json({ error: 'Failed to fetch daemons' });
  }
});

// Get market data for system
router.get('/markets/:systemSymbol', async (req, res) => {
  try {
    const db = getDatabase();
    const systemSymbol = req.params.systemSymbol;

    const markets = db.prepare(`
      SELECT
        waypoint_symbol,
        good_symbol,
        supply,
        activity,
        purchase_price,
        sell_price,
        trade_volume,
        last_updated
      FROM market_data
      WHERE waypoint_symbol LIKE ?
      ORDER BY waypoint_symbol, good_symbol
    `).all(`${systemSymbol}-%`);

    db.close();

    // Group by waypoint
    const grouped: Record<string, any> = {};
    for (const row of markets as any[]) {
      if (!grouped[row.waypoint_symbol]) {
        grouped[row.waypoint_symbol] = {
          waypoint_symbol: row.waypoint_symbol,
          last_updated: row.last_updated,
          goods: [],
        };
      }
      grouped[row.waypoint_symbol].goods.push({
        good_symbol: row.good_symbol,
        supply: row.supply,
        activity: row.activity,
        purchase_price: row.purchase_price,
        sell_price: row.sell_price,
        trade_volume: row.trade_volume,
      });

      // Update last_updated to most recent
      if (new Date(row.last_updated) > new Date(grouped[row.waypoint_symbol].last_updated)) {
        grouped[row.waypoint_symbol].last_updated = row.last_updated;
      }
    }

    res.json({ markets: Object.values(grouped) });
  } catch (error) {
    console.error('Failed to fetch market data:', error);
    res.status(500).json({ error: 'Failed to fetch market data' });
  }
});

// Get market freshness (last updated times)
router.get('/markets/:systemSymbol/freshness', async (req, res) => {
  try {
    const db = getDatabase();
    const systemSymbol = req.params.systemSymbol;

    const freshness = db.prepare(`
      SELECT
        waypoint_symbol,
        MAX(last_updated) as last_updated
      FROM market_data
      WHERE waypoint_symbol LIKE ?
      GROUP BY waypoint_symbol
    `).all(`${systemSymbol}-%`);

    db.close();

    res.json({ freshness });
  } catch (error) {
    console.error('Failed to fetch market freshness:', error);
    res.status(500).json({ error: 'Failed to fetch market freshness' });
  }
});

// Get scout tours for system (fetch individually optimized tours from cache)
router.get('/tours/:systemSymbol', async (req, res) => {
  try {
    const db = getDatabase();
    const systemSymbol = req.params.systemSymbol;
    const playerId = req.query.player_id ? parseInt(req.query.player_id as string, 10) : null;

    // Get active scout assignments (optionally filtered by player_id)
    const assignments = db.prepare(`
      SELECT
        ship_symbol,
        daemon_id,
        json_extract(metadata, '$.markets') as markets,
        assigned_at,
        player_id
      FROM ship_assignments
      WHERE daemon_id LIKE 'scout%'
        AND status = 'active'
        AND json_extract(metadata, '$.system') = ?
        AND (? IS NULL OR player_id = ?)
      ORDER BY ship_symbol
    `).all(systemSymbol, playerId, playerId);

    // For each scout assignment, find its individually optimized tour from cache
    // Match by EXACT market list (sorted) to ensure we get the correct optimized tour
    const tours = assignments.map((a: any) => {
      const assignedMarkets = JSON.parse(a.markets);

      // CRITICAL: Cache key must match daemon's format (ortools_router.py:618-621)
      // Daemon removes start waypoint from markets list before caching (line 606).
      // Formula: cache_key = (system, markets_excluding_start, algorithm, start_waypoint)
      // Example: 26 assigned markets → cache with 25 markets + start parameter

      // Extract first market as tour start (matches operations/routing.py:371)
      const tourStart = assignedMarkets[0];

      // Remove start from markets for cache lookup (matches daemon behavior at ortools_router.py:606)
      const marketsForLookup = assignedMarkets.filter((m: string) => m !== tourStart);

      // Sort markets for cache key matching (database stores sorted markets)
      // Note: Python's json.dumps() adds spaces after colons/commas, so we must match that format
      const marketsSorted = JSON.stringify(marketsForLookup.sort(), null, 0).replace(/,/g, ', ');

      // Find cached tour that exactly matches this scout's markets
      // CRITICAL: Daemon caches with different start_waypoint based on return_to_start flag:
      //   - return_to_start=True  → start_waypoint IS NULL
      //   - return_to_start=False → start_waypoint = tourStart
      // Try both patterns (prefer return_to_start=True which is typical for scouts)
      let cachedTour = db.prepare(`
        SELECT
          system,
          markets,
          algorithm,
          start_waypoint,
          tour_order,
          total_distance,
          calculated_at
        FROM tour_cache
        WHERE system = ?
          AND markets = ?
          AND algorithm IN ('ortools', '2opt')
          AND start_waypoint IS NULL
        ORDER BY
          CASE algorithm
            WHEN 'ortools' THEN 1
            WHEN '2opt' THEN 2
            ELSE 3
          END,
          calculated_at DESC
        LIMIT 1
      `).get(systemSymbol, marketsSorted);

      // If not found with NULL start_waypoint, try with specific start
      if (!cachedTour) {
        cachedTour = db.prepare(`
          SELECT
            system,
            markets,
            algorithm,
            start_waypoint,
            tour_order,
            total_distance,
            calculated_at
          FROM tour_cache
          WHERE system = ?
            AND markets = ?
            AND algorithm IN ('ortools', '2opt')
            AND start_waypoint = ?
          ORDER BY
            CASE algorithm
              WHEN 'ortools' THEN 1
              WHEN '2opt' THEN 2
              ELSE 3
            END,
            calculated_at DESC
          LIMIT 1
        `).get(systemSymbol, marketsSorted, tourStart);
      }

      if (cachedTour) {
        // Found exact cached tour for this scout's markets - use it directly
        const tour = cachedTour as any;
        const tourOrder = JSON.parse(tour.tour_order);

        return {
          system: tour.system,
          markets: assignedMarkets,
          algorithm: tour.algorithm,
          start_waypoint: tour.start_waypoint,
          tour_order: tourOrder,
          total_distance: tour.total_distance,
          calculated_at: tour.calculated_at,
          ship_symbol: a.ship_symbol,
          daemon_id: a.daemon_id,
          player_id: a.player_id,
        };
      } else {
        // No cached tour found for this exact market list
        // Return unoptimized tour_order as fallback
        // DO NOT filter full tour - that causes crossing edges!
        return {
          system: systemSymbol,
          markets: assignedMarkets,
          algorithm: 'unoptimized',
          start_waypoint: assignedMarkets[0],
          tour_order: assignedMarkets, // Use unoptimized assignment order
          total_distance: 0,
          calculated_at: a.assigned_at,
          ship_symbol: a.ship_symbol,
          daemon_id: a.daemon_id,
          player_id: a.player_id,
        };
      }
    });

    db.close();

    res.json({ tours });
  } catch (error) {
    console.error('Failed to fetch tours:', error);
    res.status(500).json({ error: 'Failed to fetch tours' });
  }
});

// Get trade opportunities (price deltas)
router.get('/trade-opportunities/:systemSymbol', async (req, res) => {
  try {
    const db = getDatabase();
    const systemSymbol = req.params.systemSymbol;
    const minProfit = parseInt(req.query.minProfit as string) || 100;

    // Find buy/sell opportunities
    const opportunities = db.prepare(`
      SELECT
        buy.waypoint_symbol as buy_waypoint,
        sell.waypoint_symbol as sell_waypoint,
        buy.good_symbol,
        buy.purchase_price as buy_price,
        sell.sell_price as sell_price,
        (sell.sell_price - buy.purchase_price) as profit_per_unit,
        buy.supply,
        sell.activity,
        buy.last_updated as buy_updated,
        sell.last_updated as sell_updated
      FROM market_data buy
      JOIN market_data sell
        ON buy.good_symbol = sell.good_symbol
        AND buy.waypoint_symbol != sell.waypoint_symbol
      WHERE buy.waypoint_symbol LIKE ?
        AND sell.waypoint_symbol LIKE ?
        AND buy.purchase_price > 0
        AND sell.sell_price > 0
        AND (sell.sell_price - buy.purchase_price) >= ?
      ORDER BY profit_per_unit DESC
      LIMIT 50
    `).all(`${systemSymbol}-%`, `${systemSymbol}-%`, minProfit);

    db.close();

    res.json({ opportunities });
  } catch (error) {
    console.error('Failed to fetch trade opportunities:', error);
    res.status(500).json({ error: 'Failed to fetch trade opportunities' });
  }
});

// Get market transactions (recent trades)
router.get('/transactions/:systemSymbol', async (req, res) => {
  try {
    const db = getDatabase();
    const systemSymbol = req.params.systemSymbol;
    const limit = parseInt(req.query.limit as string) || 100;

    const transactions = db.prepare(`
      SELECT
        ship_symbol,
        waypoint_symbol,
        good_symbol,
        transaction_type,
        units,
        price_per_unit,
        total_cost,
        timestamp
      FROM market_transactions
      WHERE waypoint_symbol LIKE ?
      ORDER BY timestamp DESC
      LIMIT ?
    `).all(`${systemSymbol}-%`, limit);

    db.close();

    res.json({ transactions });
  } catch (error) {
    console.error('Failed to fetch transactions:', error);
    res.status(500).json({ error: 'Failed to fetch transactions' });
  }
});

// Get system navigation graph
router.get('/graph/:systemSymbol', async (req, res) => {
  try {
    const db = getDatabase();
    const systemSymbol = req.params.systemSymbol;

    const graph = db.prepare(`
      SELECT
        system_symbol,
        graph_data,
        created_at,
        updated_at
      FROM system_graphs
      WHERE system_symbol = ?
    `).get(systemSymbol);

    db.close();

    if (!graph) {
      return res.status(404).json({ error: 'Graph not found' });
    }

    const parsed = {
      ...graph,
      graph_data: JSON.parse((graph as any).graph_data),
    };

    res.json({ graph: parsed });
  } catch (error) {
    console.error('Failed to fetch graph:', error);
    res.status(500).json({ error: 'Failed to fetch graph' });
  }
});

// Get operations summary
router.get('/operations/summary', async (req, res) => {
  try {
    const db = getDatabase();

    // Count by operation type
    const summary = db.prepare(`
      SELECT
        operation,
        COUNT(*) as count,
        status
      FROM ship_assignments
      GROUP BY operation, status
    `).all();

    db.close();

    res.json({ summary });
  } catch (error) {
    console.error('Failed to fetch operations summary:', error);
    res.status(500).json({ error: 'Failed to fetch operations summary' });
  }
});

// Get agent to player_id mappings
router.get('/players', async (req, res) => {
  try {
    const db = getDatabase();

    const players = db.prepare(`
      SELECT
        player_id,
        agent_symbol
      FROM players
      ORDER BY player_id
    `).all();

    db.close();

    res.json({ players });
  } catch (error) {
    console.error('Failed to fetch players:', error);
    res.status(500).json({ error: 'Failed to fetch players' });
  }
});

export default router;
