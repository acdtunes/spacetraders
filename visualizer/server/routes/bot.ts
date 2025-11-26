import { Router } from 'express';
import pkg from 'pg';
const { Pool } = pkg;
import { optimizeTour } from '../utils/tourOptimizer.js';
import { SpaceTradersClient } from '../src/client.js';
import * as db from '../db/storage.js';

const router = Router();
const API_BASE_URL = 'https://api.spacetraders.io/v2';

// Normalize operation type for display
function normalizeOperationType(opType: string | null): string {
  if (!opType) return 'unassigned';

  const normalizations: Record<string, string> = {
    'manufacturing_arbitrage': 'manufacturing',
  };

  return normalizations[opType] || opType;
}

// PostgreSQL connection pool
const pool = new Pool({
  connectionString: process.env.DATABASE_URL || 'postgresql://spacetraders:dev_password@localhost:5432/spacetraders'
});

// Get all ship assignments (Go bot - uses ship_assignments table as source of truth)
router.get('/assignments', async (req, res) => {
  const client = await pool.connect();
  try {
    // Get all agents to fetch their ships
    const agents = await db.getAllAgents();

    // Get ship assignments with container details
    const assignmentsResult = await client.query(`
      SELECT
        sa.ship_symbol,
        sa.player_id,
        sa.container_id,
        sa.status,
        sa.assigned_at,
        sa.released_at,
        c.config,
        c.container_type
      FROM ship_assignments sa
      LEFT JOIN containers c ON sa.container_id = c.id AND sa.player_id = c.player_id
    `);

    // Create a map of ship symbols to assignments
    const assignmentsByShip = new Map();
    assignmentsResult.rows.forEach((row: any) => {
      assignmentsByShip.set(row.ship_symbol, row);
    });

    // Get player_id mapping for agents
    const playerMappingsResult = await client.query(`
      SELECT id as player_id, agent_symbol
      FROM players
    `);
    const agentToPlayerMap = new Map<string, number>();
    playerMappingsResult.rows.forEach((row: any) => {
      agentToPlayerMap.set(row.agent_symbol, row.player_id);
    });

    // Fetch all ships from SpaceTraders API and merge with assignments
    const assignments = [];
    const processedShips = new Set<string>();

    for (const agent of agents) {
      try {
        const stClient = new SpaceTradersClient(API_BASE_URL, agent.token);

        // Fetch all pages of ships
        let ships: any[] = [];
        let page = 1;
        let hasMorePages = true;

        while (hasMorePages) {
          const shipsResponse = await stClient.get(`/my/ships?page=${page}&limit=20`);
          ships = ships.concat(shipsResponse.data);

          // Check if there are more pages
          const meta = shipsResponse.meta;
          if (meta && meta.total && meta.page * meta.limit >= meta.total) {
            hasMorePages = false;
          } else if (!shipsResponse.data || shipsResponse.data.length === 0) {
            hasMorePages = false;
          } else {
            page++;
          }
        }

        const playerId = agentToPlayerMap.get(agent.symbol);

        for (const ship of ships) {
          processedShips.add(ship.symbol);
          const assignment = assignmentsByShip.get(ship.symbol);

          if (assignment && assignment.status === 'active' && assignment.container_id) {
            // Ship has active assignment with container
            const config = typeof assignment.config === 'string' ? JSON.parse(assignment.config) : assignment.config;

            // Map container_type to operation name
            let operation = 'idle';
            if (assignment.container_type === 'SCOUT') {
              operation = 'scout-markets';
            } else if (assignment.container_type === 'CONTRACT' ||
                       assignment.container_type === 'CONTRACT_FLEET_COORDINATOR') {
              operation = 'contract';
            } else if (assignment.container_type === 'CONTRACT_WORKFLOW') {
              operation = 'contract';
            } else if (assignment.container_type === 'PURCHASE') {
              operation = 'shipyard';
            } else if (assignment.container_type === 'MINING_COORDINATOR' ||
                       assignment.container_type === 'MINING_WORKER') {
              operation = 'mine';
            } else if (assignment.container_type === 'TRANSPORT_WORKER') {
              operation = 'transport';
            } else if (assignment.container_type === 'goods_factory_coordinator') {
              operation = 'factory';
            } else if (assignment.container_type === 'ARBITRAGE_COORDINATOR' ||
                       assignment.container_type === 'ARBITRAGE_WORKER') {
              operation = 'arbitrage';
            } else if (assignment.container_type === 'MANUFACTURING_TASK_WORKER') {
              operation = 'manufacturing';
            }

            assignments.push({
              ship_symbol: ship.symbol,
              player_id: assignment.player_id,
              assigned_to: assignment.container_id,
              daemon_id: assignment.container_id,
              status: assignment.status,
              assigned_at: assignment.assigned_at,
              released_at: assignment.released_at,
              metadata: config,
              operation,
            });
          } else {
            // Ship is idle (not in ship_assignments or no container)
            assignments.push({
              ship_symbol: ship.symbol,
              player_id: playerId,
              assigned_to: null,
              daemon_id: null,
              status: 'active',
              assigned_at: null,
              released_at: null,
              metadata: null,
              operation: 'idle',
            });
          }
        }
      } catch (error) {
        console.error(`Failed to fetch ships for agent ${agent.symbol}:`, error);
      }
    }

    // Add ships that have assignments but weren't found in SpaceTraders API
    // (e.g., newly purchased ships that haven't appeared in API yet)
    assignmentsResult.rows.forEach((row: any) => {
      if (row.status === 'active' && row.container_id && !processedShips.has(row.ship_symbol)) {
        const config = typeof row.config === 'string' ? JSON.parse(row.config) : row.config;

        // Map container_type to operation name
        let operation = 'idle';
        if (row.container_type === 'SCOUT') {
          operation = 'scout-markets';
        } else if (row.container_type === 'CONTRACT' ||
                   row.container_type === 'CONTRACT_FLEET_COORDINATOR') {
          operation = 'contract';
        } else if (row.container_type === 'CONTRACT_WORKFLOW') {
          operation = 'contract';
        } else if (row.container_type === 'PURCHASE') {
          operation = 'shipyard';
        } else if (row.container_type === 'MINING_COORDINATOR' ||
                   row.container_type === 'MINING_WORKER') {
          operation = 'mine';
        } else if (row.container_type === 'TRANSPORT_WORKER') {
          operation = 'transport';
        } else if (row.container_type === 'goods_factory_coordinator') {
          operation = 'factory';
        } else if (row.container_type === 'ARBITRAGE_COORDINATOR' ||
                   row.container_type === 'ARBITRAGE_WORKER') {
          operation = 'arbitrage';
        } else if (row.container_type === 'MANUFACTURING_TASK_WORKER') {
          operation = 'manufacturing';
        }

        assignments.push({
          ship_symbol: row.ship_symbol,
          player_id: row.player_id,
          assigned_to: row.container_id,
          daemon_id: row.container_id,
          status: row.status,
          assigned_at: row.assigned_at,
          released_at: row.released_at,
          metadata: config,
          operation,
        });
      }
    });

    res.json({ assignments });
  } catch (error) {
    console.error('Failed to fetch assignments:', error);
    res.status(500).json({ error: 'Failed to fetch assignments' });
  } finally {
    client.release();
  }
});

// Get assignment for specific ship (Go bot - queries ship_assignments)
router.get('/assignments/:shipSymbol', async (req, res) => {
  const client = await pool.connect();
  try {
    const result = await client.query(`
      SELECT
        sa.ship_symbol,
        sa.player_id,
        sa.container_id as assigned_to,
        sa.container_id as daemon_id,
        sa.status,
        sa.assigned_at,
        sa.released_at,
        c.config as metadata,
        c.container_type
      FROM ship_assignments sa
      LEFT JOIN containers c ON sa.container_id = c.id AND sa.player_id = c.player_id
      WHERE sa.ship_symbol = $1
    `, [req.params.shipSymbol]);

    if (result.rows.length === 0) {
      // Ship not in ship_assignments table - it's idle
      return res.json({
        assignment: {
          ship_symbol: req.params.shipSymbol,
          player_id: null,
          assigned_to: null,
          daemon_id: null,
          status: 'active',
          assigned_at: null,
          released_at: null,
          metadata: null,
          operation: 'idle',
        }
      });
    }

    const assignment = result.rows[0];

    if (assignment.status === 'active' && assignment.daemon_id) {
      const config = typeof assignment.metadata === 'string' ? JSON.parse(assignment.metadata) : assignment.metadata;

      // Map container_type to operation name
      let operation = 'idle';
      if (assignment.container_type === 'SCOUT') {
        operation = 'scout-markets';
      } else if (assignment.container_type === 'CONTRACT' ||
                 assignment.container_type === 'CONTRACT_FLEET_COORDINATOR') {
        operation = 'contract';
      } else if (assignment.container_type === 'CONTRACT_WORKFLOW') {
        operation = 'contract';
      } else if (assignment.container_type === 'PURCHASE') {
        operation = 'shipyard';
      } else if (assignment.container_type === 'MINING_COORDINATOR' ||
                 assignment.container_type === 'MINING_WORKER') {
        operation = 'mine';
      } else if (assignment.container_type === 'TRANSPORT_WORKER') {
        operation = 'transport';
      } else if (assignment.container_type === 'goods_factory_coordinator') {
        operation = 'factory';
      } else if (assignment.container_type === 'ARBITRAGE_COORDINATOR' ||
                 assignment.container_type === 'ARBITRAGE_WORKER') {
        operation = 'arbitrage';
      } else if (assignment.container_type === 'MANUFACTURING_TASK_WORKER') {
        operation = 'manufacturing';
      }

      const parsed = {
        ...assignment,
        metadata: config,
        operation,
      };

      res.json({ assignment: parsed });
    } else {
      // Assignment exists but ship is idle
      res.json({
        assignment: {
          ...assignment,
          metadata: null,
          operation: 'idle',
        }
      });
    }
  } catch (error) {
    console.error('Failed to fetch assignment:', error);
    res.status(500).json({ error: 'Failed to fetch assignment' });
  } finally {
    client.release();
  }
});

// Get all active containers (daemons)
router.get('/daemons', async (req, res) => {
  const client = await pool.connect();
  try {
    const result = await client.query(`
      SELECT
        id as daemon_id,
        player_id,
        NULL as pid,
        config as command,
        started_at,
        stopped_at,
        status,
        NULL as log_file,
        NULL as err_file
      FROM containers
      WHERE status IN ('RUNNING', 'STOPPING', 'STARTED')
    `);

    // Config is already JSONB in PostgreSQL
    const parsed = result.rows.map((d: any) => ({
      ...d,
      command: d.command || null,
    }));

    res.json({ daemons: parsed });
  } catch (error) {
    console.error('Failed to fetch daemons:', error);
    res.status(500).json({ error: 'Failed to fetch daemons' });
  } finally {
    client.release();
  }
});

// Get market data for system
router.get('/markets/:systemSymbol', async (req, res) => {
  const client = await pool.connect();
  try {
    const systemSymbol = req.params.systemSymbol;

    const result = await client.query(`
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
      WHERE waypoint_symbol LIKE $1
      ORDER BY waypoint_symbol, good_symbol
    `, [`${systemSymbol}-%`]);

    // Group by waypoint
    const grouped: Record<string, any> = {};
    for (const row of result.rows) {
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
  } finally {
    client.release();
  }
});

// Get market freshness (last updated times)
router.get('/markets/:systemSymbol/freshness', async (req, res) => {
  const client = await pool.connect();
  try {
    const systemSymbol = req.params.systemSymbol;

    const result = await client.query(`
      SELECT
        waypoint_symbol,
        MAX(last_updated) as last_updated
      FROM market_data
      WHERE waypoint_symbol LIKE $1
      GROUP BY waypoint_symbol
    `, [`${systemSymbol}-%`]);

    res.json({ freshness: result.rows });
  } catch (error) {
    console.error('Failed to fetch market freshness:', error);
    res.status(500).json({ error: 'Failed to fetch market freshness' });
  } finally {
    client.release();
  }
});

// Get scout tours for system (extract ACTUAL optimized tours from container logs)
router.get('/tours/:systemSymbol', async (req, res) => {
  const client = await pool.connect();
  try {
    const systemSymbol = req.params.systemSymbol;
    const playerId = req.query.player_id ? parseInt(req.query.player_id as string, 10) : null;

    // Get system graph for waypoint coordinates (for distance calculation)
    const graphResult = await client.query(`
      SELECT graph_data
      FROM system_graphs
      WHERE system_symbol = $1
    `, [systemSymbol]);

    if (graphResult.rows.length === 0) {
      return res.status(404).json({ error: 'System graph not found' });
    }

    // Parse graph_data if it's a string
    const graphData = typeof graphResult.rows[0].graph_data === 'string'
      ? JSON.parse(graphResult.rows[0].graph_data)
      : graphResult.rows[0].graph_data;
    const waypoints = graphData.waypoints || {};

    // Get Go bot scout tour containers (identified by id pattern)
    const assignmentsResult = await client.query(`
      SELECT
        c.config::jsonb->>'ship_symbol' as ship_symbol,
        c.id as daemon_id,
        c.config,
        c.started_at as assigned_at,
        c.player_id
      FROM containers c
      WHERE (c.id LIKE 'scout_tour-%' OR c.id LIKE 'scout-tour-%')
        AND c.status IN ('RUNNING', 'STARTING', 'STARTED')
        AND ($1::integer IS NULL OR c.player_id = $1)
      ORDER BY ship_symbol
    `, [playerId]);

    // For each Go bot scout tour, extract the tour details
    const tours = [];
    for (const a of assignmentsResult.rows) {
      try {
        // Parse config if it's a string
        const config = typeof a.config === 'string' ? JSON.parse(a.config) : a.config;

        const markets = config.markets || [];
        if (markets.length === 0) {
          continue;
        }

        // Extract system from first market waypoint (e.g., "X1-TS98-J56" -> "X1-TS98")
        const parts = markets[0].split('-');
        const tourSystem = parts.length >= 2 ? `${parts[0]}-${parts[1]}` : null;

        // Filter by system
        if (tourSystem !== systemSymbol) {
          continue;
        }

        // Get ACTUAL optimized tour order from container logs
        const startLogResult = await client.query(`
          SELECT timestamp
          FROM container_logs
          WHERE container_id = $1
          AND message LIKE 'Starting scout tour: ' || $2 || '%'
          ORDER BY timestamp DESC
          LIMIT 1
        `, [a.daemon_id, a.ship_symbol]);

        let tourOrder = markets; // Fallback to config order
        let algorithm = 'config-order';

        if (startLogResult.rows.length > 0) {
          const startLog = startLogResult.rows[0];
          const expectedVisits = markets.length;

          const visitLogsResult = await client.query(`
            SELECT message, timestamp
            FROM container_logs
            WHERE container_id = $1
            AND message LIKE 'Visiting market%'
            AND timestamp > $2
            AND timestamp < (
              SELECT COALESCE(MIN(timestamp), NOW())
              FROM container_logs
              WHERE container_id = $1
              AND message LIKE 'Starting scout tour%'
              AND timestamp > $2
            )
            ORDER BY timestamp ASC
            LIMIT $3
          `, [a.daemon_id, startLog.timestamp, expectedVisits]);

          // Extract waypoints from "Visiting market 1/6: X1-GZ7-B6" format
          const extractedOrder: string[] = [];
          for (const log of visitLogsResult.rows) {
            const match = log.message.match(/Visiting market \d+\/\d+: (.+)$/);
            if (match) {
              extractedOrder.push(match[1].trim());
            }
          }

          // Only use extracted order if we have a COMPLETE tour (all markets visited)
          // Incomplete tours mean the ship is mid-tour, so fall back to config order
          if (extractedOrder.length === expectedVisits) {
            tourOrder = extractedOrder;
            algorithm = 'ortools-vrp';
          }
        }

        // Make tour cyclical if it isn't already (append starting waypoint to end)
        if (tourOrder.length > 0 && tourOrder[0] !== tourOrder[tourOrder.length - 1]) {
          tourOrder.push(tourOrder[0]);
        }

        // Calculate total distance (including return to start)
        let totalDistance = 0;
        for (let i = 0; i < tourOrder.length - 1; i++) {
          const from = waypoints[tourOrder[i]];
          const to = waypoints[tourOrder[i + 1]];
          if (from && to) {
            const dx = to.x - from.x;
            const dy = to.y - from.y;
            totalDistance += Math.sqrt(dx * dx + dy * dy);
          }
        }

        tours.push({
          system: tourSystem,
          markets: markets,
          algorithm: algorithm,
          start_waypoint: tourOrder[0] || null,
          tour_order: tourOrder,
          total_distance: Math.round(totalDistance * 100) / 100,
          calculated_at: a.assigned_at,
          ship_symbol: a.ship_symbol,
          daemon_id: a.daemon_id,
          player_id: a.player_id,
        });
      } catch (error) {
        console.warn(`Failed to parse config for ${a.ship_symbol}:`, error);
      }
    }

    res.json({ tours });
  } catch (error) {
    console.error('Failed to fetch tours:', error);
    res.status(500).json({ error: 'Failed to fetch tours' });
  } finally {
    client.release();
  }
});

// Get trade opportunities (price deltas)
router.get('/trade-opportunities/:systemSymbol', async (req, res) => {
  const client = await pool.connect();
  try {
    const systemSymbol = req.params.systemSymbol;
    const minProfit = parseInt(req.query.minProfit as string) || 100;

    const result = await client.query(`
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
      WHERE buy.waypoint_symbol LIKE $1
        AND sell.waypoint_symbol LIKE $1
        AND buy.purchase_price > 0
        AND sell.sell_price > 0
        AND (sell.sell_price - buy.purchase_price) >= $2
      ORDER BY profit_per_unit DESC
      LIMIT 50
    `, [`${systemSymbol}-%`, minProfit]);

    res.json({ opportunities: result.rows });
  } catch (error) {
    console.error('Failed to fetch trade opportunities:', error);
    res.status(500).json({ error: 'Failed to fetch trade opportunities' });
  } finally {
    client.release();
  }
});

// Get market transactions (recent trades)
router.get('/transactions/:systemSymbol', async (req, res) => {
  const client = await pool.connect();
  try {
    const systemSymbol = req.params.systemSymbol;
    const limit = parseInt(req.query.limit as string) || 100;

    const result = await client.query(`
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
      WHERE waypoint_symbol LIKE $1
      ORDER BY timestamp DESC
      LIMIT $2
    `, [`${systemSymbol}-%`, limit]);

    res.json({ transactions: result.rows });
  } catch (error) {
    console.error('Failed to fetch transactions:', error);
    res.status(500).json({ error: 'Failed to fetch transactions' });
  } finally {
    client.release();
  }
});

// Get system navigation graph
router.get('/graph/:systemSymbol', async (req, res) => {
  const client = await pool.connect();
  try {
    const systemSymbol = req.params.systemSymbol;

    const result = await client.query(`
      SELECT
        system_symbol,
        graph_data,
        last_updated
      FROM system_graphs
      WHERE system_symbol = $1
    `, [systemSymbol]);

    if (result.rows.length === 0) {
      return res.status(404).json({ error: 'Graph not found' });
    }

    // Parse graph_data if it's a string
    const graph = result.rows[0];
    const parsed = {
      ...graph,
      graph_data: typeof graph.graph_data === 'string' ? JSON.parse(graph.graph_data) : graph.graph_data
    };

    res.json({ graph: parsed });
  } catch (error) {
    console.error('Failed to fetch graph:', error);
    res.status(500).json({ error: 'Failed to fetch graph' });
  } finally {
    client.release();
  }
});

// Get operations summary
router.get('/operations/summary', async (req, res) => {
  const client = await pool.connect();
  try {
    const result = await client.query(`
      SELECT
        operation,
        COUNT(*) as count,
        status
      FROM ship_assignments
      GROUP BY operation, status
    `);

    res.json({ summary: result.rows });
  } catch (error) {
    console.error('Failed to fetch operations summary:', error);
    res.status(500).json({ error: 'Failed to fetch operations summary' });
  } finally {
    client.release();
  }
});

// Get agent to player_id mappings
router.get('/players', async (req, res) => {
  const client = await pool.connect();
  try {
    const result = await client.query(`
      SELECT
        id as player_id,
        agent_symbol
      FROM players
      ORDER BY id
    `);

    res.json({ players: result.rows });
  } catch (error) {
    console.error('Failed to fetch players:', error);
    res.status(500).json({ error: 'Failed to fetch players' });
  } finally {
    client.release();
  }
});

// ==================== Financial Ledger Endpoints ====================

// Get financial transactions
router.get('/ledger/transactions', async (req, res) => {
  const client = await pool.connect();
  try {
    const playerId = req.query.player_id ? parseInt(req.query.player_id as string, 10) : null;
    const limit = Math.min(parseInt(req.query.limit as string) || 50, 1000);
    const offset = parseInt(req.query.offset as string) || 0;
    const category = req.query.category as string | undefined;
    const type = req.query.type as string | undefined;
    const startDate = req.query.start_date as string | undefined;
    const endDate = req.query.end_date as string | undefined;
    const search = req.query.search as string | undefined;

    if (!playerId) {
      return res.status(400).json({ error: 'player_id is required' });
    }

    // Validate date range
    if (startDate && endDate && new Date(startDate) > new Date(endDate)) {
      return res.status(400).json({ error: 'start_date must be before end_date' });
    }

    // Build dynamic query
    const params: any[] = [playerId];
    let paramIndex = 2;
    let whereConditions = ['player_id = $1'];

    if (category) {
      whereConditions.push(`category = $${paramIndex}`);
      params.push(category);
      paramIndex++;
    }

    if (type) {
      whereConditions.push(`transaction_type = $${paramIndex}`);
      params.push(type);
      paramIndex++;
    }

    if (startDate) {
      whereConditions.push(`timestamp >= $${paramIndex}`);
      params.push(startDate);
      paramIndex++;
    }

    if (endDate) {
      whereConditions.push(`timestamp <= $${paramIndex}`);
      params.push(endDate);
      paramIndex++;
    }

    if (search) {
      whereConditions.push(`description ILIKE $${paramIndex}`);
      params.push(`%${search}%`);
      paramIndex++;
    }

    const whereClause = whereConditions.join(' AND ');

    // Get total count
    const countResult = await client.query(
      `SELECT COUNT(*) as total FROM transactions WHERE ${whereClause}`,
      params
    );
    const total = parseInt(countResult.rows[0].total, 10);

    // Get transactions
    const result = await client.query(`
      SELECT
        id,
        player_id,
        timestamp,
        transaction_type,
        category,
        amount,
        balance_before,
        balance_after,
        description,
        metadata,
        related_entity_type,
        related_entity_id
      FROM transactions
      WHERE ${whereClause}
      ORDER BY timestamp DESC
      LIMIT $${paramIndex} OFFSET $${paramIndex + 1}
    `, [...params, limit, offset]);

    const page = Math.floor(offset / limit) + 1;

    res.json({
      transactions: result.rows,
      total,
      page,
      limit
    });
  } catch (error) {
    console.error('Failed to fetch transactions:', error);
    res.status(500).json({ error: 'Failed to fetch transactions' });
  } finally {
    client.release();
  }
});

// Get cash flow analysis
router.get('/ledger/cash-flow', async (req, res) => {
  const client = await pool.connect();
  try {
    const playerId = req.query.player_id ? parseInt(req.query.player_id as string, 10) : null;
    const startDate = req.query.start_date as string | undefined;
    const endDate = req.query.end_date as string | undefined;

    if (!playerId) {
      return res.status(400).json({ error: 'player_id is required' });
    }

    // Validate date range
    if (startDate && endDate && new Date(startDate) > new Date(endDate)) {
      return res.status(400).json({ error: 'start_date must be before end_date' });
    }

    const params: any[] = [playerId];
    let paramIndex = 2;
    let whereConditions = ['player_id = $1'];

    if (startDate) {
      whereConditions.push(`timestamp >= $${paramIndex}`);
      params.push(startDate);
      paramIndex++;
    }

    if (endDate) {
      whereConditions.push(`timestamp <= $${paramIndex}`);
      params.push(endDate);
      paramIndex++;
    }

    const whereClause = whereConditions.join(' AND ');

    // Get period bounds
    const periodResult = await client.query(`
      SELECT
        MIN(timestamp) as start,
        MAX(timestamp) as end
      FROM transactions
      WHERE ${whereClause}
    `, params);

    // Get category breakdown
    const categoriesResult = await client.query(`
      SELECT
        category,
        SUM(CASE WHEN amount > 0 THEN amount ELSE 0 END) as total_inflow,
        SUM(CASE WHEN amount < 0 THEN amount ELSE 0 END) as total_outflow,
        SUM(amount) as net_flow,
        COUNT(*) as transaction_count
      FROM transactions
      WHERE ${whereClause}
      GROUP BY category
      ORDER BY net_flow DESC
    `, params);

    // Calculate summary
    const summary = categoriesResult.rows.reduce((acc, row) => ({
      total_inflow: acc.total_inflow + parseFloat(row.total_inflow),
      total_outflow: acc.total_outflow + parseFloat(row.total_outflow),
      net_cash_flow: acc.net_cash_flow + parseFloat(row.net_flow)
    }), { total_inflow: 0, total_outflow: 0, net_cash_flow: 0 });

    res.json({
      period: {
        start: periodResult.rows[0].start || (startDate || null),
        end: periodResult.rows[0].end || (endDate || null)
      },
      summary,
      categories: categoriesResult.rows
    });
  } catch (error) {
    console.error('Failed to fetch cash flow:', error);
    res.status(500).json({ error: 'Failed to fetch cash flow' });
  } finally {
    client.release();
  }
});

// Get profit & loss statement
router.get('/ledger/profit-loss', async (req, res) => {
  const client = await pool.connect();
  try {
    const playerId = req.query.player_id ? parseInt(req.query.player_id as string, 10) : null;
    const startDate = req.query.start_date as string | undefined;
    const endDate = req.query.end_date as string | undefined;

    if (!playerId) {
      return res.status(400).json({ error: 'player_id is required' });
    }

    // Validate date range
    if (startDate && endDate && new Date(startDate) > new Date(endDate)) {
      return res.status(400).json({ error: 'start_date must be before end_date' });
    }

    const params: any[] = [playerId];
    let paramIndex = 2;
    let whereConditions = ['player_id = $1'];

    if (startDate) {
      whereConditions.push(`timestamp >= $${paramIndex}`);
      params.push(startDate);
      paramIndex++;
    }

    if (endDate) {
      whereConditions.push(`timestamp <= $${paramIndex}`);
      params.push(endDate);
      paramIndex++;
    }

    const whereClause = whereConditions.join(' AND ');

    // Get period bounds
    const periodResult = await client.query(`
      SELECT
        MIN(timestamp) as start,
        MAX(timestamp) as end
      FROM transactions
      WHERE ${whereClause}
    `, params);

    // Get revenue (positive amounts)
    const revenueResult = await client.query(`
      SELECT
        category,
        SUM(amount) as total
      FROM transactions
      WHERE ${whereClause}
        AND amount > 0
      GROUP BY category
    `, params);

    // Get expenses (negative amounts)
    const expensesResult = await client.query(`
      SELECT
        category,
        SUM(amount) as total
      FROM transactions
      WHERE ${whereClause}
        AND amount < 0
      GROUP BY category
    `, params);

    // Build revenue breakdown
    const revenueBreakdown: Record<string, number> = {};
    let totalRevenue = 0;
    revenueResult.rows.forEach(row => {
      const amount = parseFloat(row.total);
      revenueBreakdown[row.category] = amount;
      totalRevenue += amount;
    });

    // Build expenses breakdown
    const expensesBreakdown: Record<string, number> = {};
    let totalExpenses = 0;
    expensesResult.rows.forEach(row => {
      const amount = parseFloat(row.total);
      expensesBreakdown[row.category] = amount;
      totalExpenses += amount;
    });

    const netProfit = totalRevenue + totalExpenses; // expenses are negative
    const profitMargin = totalRevenue > 0 ? netProfit / totalRevenue : 0;

    res.json({
      period: {
        start: periodResult.rows[0].start || (startDate || null),
        end: periodResult.rows[0].end || (endDate || null)
      },
      revenue: {
        total: totalRevenue,
        breakdown: revenueBreakdown
      },
      expenses: {
        total: totalExpenses,
        breakdown: expensesBreakdown
      },
      net_profit: netProfit,
      profit_margin: profitMargin
    });
  } catch (error) {
    console.error('Failed to fetch profit & loss:', error);
    res.status(500).json({ error: 'Failed to fetch profit & loss' });
  } finally {
    client.release();
  }
});

// Get profit & loss statement by operation type
router.get('/ledger/profit-loss-by-operation', async (req, res) => {
  const client = await pool.connect();
  try {
    const playerId = req.query.player_id ? parseInt(req.query.player_id as string, 10) : null;
    const startDate = req.query.start_date as string | undefined;
    const endDate = req.query.end_date as string | undefined;

    if (!playerId) {
      return res.status(400).json({ error: 'player_id is required' });
    }

    // Validate date range
    if (startDate && endDate && new Date(startDate) > new Date(endDate)) {
      return res.status(400).json({ error: 'start_date must be before end_date' });
    }

    const params: any[] = [playerId];
    let paramIndex = 2;
    let whereConditions = ['player_id = $1'];

    if (startDate) {
      whereConditions.push(`timestamp >= $${paramIndex}`);
      params.push(startDate);
      paramIndex++;
    }

    if (endDate) {
      whereConditions.push(`timestamp <= $${paramIndex}`);
      params.push(endDate);
      paramIndex++;
    }

    const whereClause = whereConditions.join(' AND ');

    // Get period bounds
    const periodResult = await client.query(`
      SELECT
        MIN(timestamp) as start,
        MAX(timestamp) as end
      FROM transactions
      WHERE ${whereClause}
    `, params);

    // Get breakdown by operation type and category
    // Normalize operation_type (e.g., manufacturing_arbitrage -> manufacturing)
    // Use NULLIF to convert empty strings to NULL, then COALESCE to 'unassigned'
    const operationBreakdownResult = await client.query(`
      SELECT
        CASE
          WHEN operation_type = 'manufacturing_arbitrage' THEN 'manufacturing'
          ELSE COALESCE(NULLIF(operation_type, ''), 'unassigned')
        END as operation_type,
        category,
        SUM(amount) as total,
        COUNT(*) as transaction_count
      FROM transactions
      WHERE ${whereClause}
      GROUP BY CASE
          WHEN operation_type = 'manufacturing_arbitrage' THEN 'manufacturing'
          ELSE COALESCE(NULLIF(operation_type, ''), 'unassigned')
        END, category
      ORDER BY operation_type, category
    `, params);

    // Get overall totals by operation
    // Normalize operation_type (e.g., manufacturing_arbitrage -> manufacturing)
    const operationTotalsResult = await client.query(`
      SELECT
        CASE
          WHEN operation_type = 'manufacturing_arbitrage' THEN 'manufacturing'
          ELSE COALESCE(operation_type, 'unassigned')
        END as operation_type,
        SUM(CASE WHEN amount > 0 THEN amount ELSE 0 END) as revenue,
        SUM(CASE WHEN amount < 0 THEN amount ELSE 0 END) as expenses,
        SUM(amount) as net_profit,
        COUNT(*) as transaction_count
      FROM transactions
      WHERE ${whereClause}
      GROUP BY CASE
          WHEN operation_type = 'manufacturing_arbitrage' THEN 'manufacturing'
          ELSE COALESCE(operation_type, 'unassigned')
        END
      ORDER BY operation_type
    `, params);

    // Build operation breakdown structure
    const operations: any[] = [];
    const operationMap = new Map();

    // Initialize operations from totals
    operationTotalsResult.rows.forEach(row => {
      const operation = {
        operation: row.operation_type,
        revenue: parseFloat(row.revenue) || 0,
        expenses: parseFloat(row.expenses) || 0,
        net_profit: parseFloat(row.net_profit) || 0,
        transaction_count: parseInt(row.transaction_count, 10),
        breakdown: {} as Record<string, number>
      };
      operations.push(operation);
      operationMap.set(row.operation_type, operation);
    });

    // Add category breakdown to each operation
    operationBreakdownResult.rows.forEach(row => {
      const operation = operationMap.get(row.operation_type);
      if (operation) {
        operation.breakdown[row.category] = parseFloat(row.total);
      }
    });

    // Calculate summary
    const summary = {
      total_revenue: 0,
      total_expenses: 0,
      net_profit: 0
    };

    operations.forEach(op => {
      summary.total_revenue += op.revenue;
      summary.total_expenses += op.expenses;
      summary.net_profit += op.net_profit;
    });

    res.json({
      period: {
        start: periodResult.rows[0].start || (startDate || null),
        end: periodResult.rows[0].end || (endDate || null)
      },
      summary,
      operations
    });
  } catch (error) {
    console.error('Failed to fetch operation-based P&L:', error);
    res.status(500).json({ error: 'Failed to fetch operation-based P&L' });
  } finally {
    client.release();
  }
});

// Get balance history
router.get('/ledger/balance-history', async (req, res) => {
  const client = await pool.connect();
  try {
    const playerId = req.query.player_id ? parseInt(req.query.player_id as string, 10) : null;
    const startDate = req.query.start_date as string | undefined;
    const endDate = req.query.end_date as string | undefined;
    const interval = req.query.interval as string | undefined;

    if (!playerId) {
      return res.status(400).json({ error: 'player_id is required' });
    }

    // Validate date range
    if (startDate && endDate && new Date(startDate) > new Date(endDate)) {
      return res.status(400).json({ error: 'start_date must be before end_date' });
    }

    const params: any[] = [playerId];
    let paramIndex = 2;
    let whereConditions = ['player_id = $1'];

    if (startDate) {
      whereConditions.push(`timestamp >= $${paramIndex}`);
      params.push(startDate);
      paramIndex++;
    }

    if (endDate) {
      whereConditions.push(`timestamp <= $${paramIndex}`);
      params.push(endDate);
      paramIndex++;
    }

    const whereClause = whereConditions.join(' AND ');

    // Get balance data points
    const result = await client.query(`
      SELECT
        timestamp,
        balance_after as balance,
        id as transaction_id,
        transaction_type,
        amount
      FROM transactions
      WHERE ${whereClause}
      ORDER BY timestamp ASC
    `, params);

    // Get current balance (latest transaction)
    const currentBalanceResult = await client.query(`
      SELECT balance_after
      FROM transactions
      WHERE player_id = $1
      ORDER BY timestamp DESC
      LIMIT 1
    `, [playerId]);

    const currentBalance = currentBalanceResult.rows.length > 0
      ? parseFloat(currentBalanceResult.rows[0].balance_after)
      : 0;

    const startingBalance = result.rows.length > 0
      ? parseFloat(result.rows[0].balance) - parseFloat(result.rows[0].amount)
      : currentBalance;

    const netChange = currentBalance - startingBalance;

    res.json({
      dataPoints: result.rows,
      current_balance: currentBalance,
      starting_balance: startingBalance,
      net_change: netChange
    });
  } catch (error) {
    console.error('Failed to fetch balance history:', error);
    res.status(500).json({ error: 'Failed to fetch balance history' });
  } finally {
    client.release();
  }
});

export default router;
