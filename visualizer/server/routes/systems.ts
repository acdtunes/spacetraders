import { Router } from 'express';
import { SpaceTradersClient } from '../src/client.js';
import { getAgent } from '../db/storage.js';

const router = Router();
const API_BASE_URL = 'https://api.spacetraders.io/v2';

// Note: Most endpoints don't require authentication tokens
// They use public SpaceTraders API endpoints
// Market data endpoint requires agent authentication

// Get all systems (with pagination support)
// IMPORTANT: This route must come before /:systemSymbol to avoid path collision
router.get('/', async (req, res) => {
  try {
    const { page = '1', limit = '20' } = req.query;
    const client = new SpaceTradersClient(API_BASE_URL);

    const params = new URLSearchParams();
    params.append('page', page as string);
    params.append('limit', limit as string);

    const systems = await client.get(`/systems?${params.toString()}`);
    res.json(systems);
  } catch (error) {
    res.status(500).json({ error: 'Failed to fetch systems' });
  }
});

// Get system details
router.get('/:systemSymbol', async (req, res) => {
  try {
    const client = new SpaceTradersClient(API_BASE_URL);
    const system = await client.get(`/systems/${req.params.systemSymbol}`);
    res.json(system);
  } catch (error) {
    res.status(500).json({ error: 'Failed to fetch system' });
  }
});

// Get waypoints in a system
router.get('/:systemSymbol/waypoints', async (req, res) => {
  try {
    const { page = '1', limit = '20' } = req.query;
    const client = new SpaceTradersClient(API_BASE_URL);

    const params = new URLSearchParams();
    params.append('page', page as string);
    params.append('limit', limit as string);

    const waypoints = await client.get(
      `/systems/${req.params.systemSymbol}/waypoints?${params.toString()}`
    );

    res.json(waypoints);
  } catch (error: any) {
    console.error('Failed to fetch waypoints:', error);

    // Check if it's a 404 (no more pages)
    if (error.response?.status === 404) {
      return res.status(404).json({ error: 'No waypoints found or invalid page' });
    }

    // Return detailed error for debugging
    const errorMessage = error.message || 'Failed to fetch waypoints';
    const statusCode = error.response?.status || 500;
    res.status(statusCode).json({
      error: errorMessage,
      details: error.response?.data || null
    });
  }
});

// Get specific waypoint
router.get('/:systemSymbol/waypoints/:waypointSymbol', async (req, res) => {
  try {
    const client = new SpaceTradersClient(API_BASE_URL);
    const waypoint = await client.get(
      `/systems/${req.params.systemSymbol}/waypoints/${req.params.waypointSymbol}`
    );
    res.json(waypoint);
  } catch (error) {
    res.status(500).json({ error: 'Failed to fetch waypoint' });
  }
});

// Get market data for a waypoint (requires agent authentication)
router.get('/:systemSymbol/waypoints/:waypointSymbol/market', async (req, res) => {
  try {
    const { agentId } = req.query;

    if (!agentId || typeof agentId !== 'string') {
      return res.status(400).json({ error: 'agentId query parameter required' });
    }

    // Retrieve agent token from storage
    const agent = await getAgent(agentId);
    if (!agent) {
      return res.status(404).json({ error: 'Agent not found' });
    }

    // Create authenticated client
    const client = new SpaceTradersClient(API_BASE_URL, agent.token);
    const market = await client.get(
      `/systems/${req.params.systemSymbol}/waypoints/${req.params.waypointSymbol}/market`
    );

    res.json(market);
  } catch (error: any) {
    if (error.response?.status === 404) {
      return res.status(404).json({ error: 'Market not found at this waypoint' });
    }
    res.status(500).json({ error: 'Failed to fetch market data' });
  }
});

export default router;
