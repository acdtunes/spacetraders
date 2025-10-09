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

// Get scout tours for system (only most recent per start_waypoint)
router.get('/tours/:systemSymbol', async (req, res) => {
  try {
    const db = getDatabase();
    const systemSymbol = req.params.systemSymbol;

    // Only get the most recent tour for each start_waypoint using window functions
    // Ranks tours by recency, breaking ties by markets (most markets first)
    const tours = db.prepare(`
      WITH RankedTours AS (
        SELECT
          system,
          markets,
          algorithm,
          start_waypoint,
          tour_order,
          total_distance,
          calculated_at,
          ROW_NUMBER() OVER (
            PARTITION BY system, start_waypoint
            ORDER BY calculated_at DESC, markets DESC
          ) as rn
        FROM tour_cache
        WHERE system = ?
      )
      SELECT
        system,
        markets,
        algorithm,
        start_waypoint,
        tour_order,
        total_distance,
        calculated_at
      FROM RankedTours
      WHERE rn = 1
    `).all(systemSymbol);

    db.close();

    // Parse JSON fields
    const parsed = tours.map((t: any) => ({
      ...t,
      markets: JSON.parse(t.markets),
      tour_order: JSON.parse(t.tour_order),
    }));

    res.json({ tours: parsed });
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

export default router;
