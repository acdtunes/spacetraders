import { Router } from 'express';
import pkg from 'pg';
const { Pool } = pkg;
import { optimizeTour } from '../utils/tourOptimizer.js';
import { SpaceTradersClient } from '../src/client.js';
import * as db from '../db/storage.js';

const router = Router();
const API_BASE_URL = 'https://api.spacetraders.io/v2';

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

export default router;
