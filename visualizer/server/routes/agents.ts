import { Router } from 'express';
import { SpaceTradersClient } from '../src/client.js';
import * as db from '../db/storage.js';

const router = Router();
const API_BASE_URL = 'https://api.spacetraders.io/v2';

// Get all agents
router.get('/', async (req, res) => {
  try {
    const agents = await db.getAllAgents();

    // Fetch credits for each agent
    const agentsWithCredits = await Promise.all(
      agents.map(async (agent) => {
        try {
          const client = new SpaceTradersClient(API_BASE_URL, agent.token);
          const agentData = await client.get('/my/agent');
          const { token, ...sanitized } = agent;
          return { ...sanitized, credits: agentData.data.credits };
        } catch (error) {
          // If credits fetch fails, return agent without credits
          const { token, ...sanitized } = agent;
          return sanitized;
        }
      })
    );

    res.json({ agents: agentsWithCredits });
  } catch (error) {
    res.status(500).json({ error: 'Failed to fetch agents' });
  }
});

// Get single agent
router.get('/:id', async (req, res) => {
  try {
    const agent = await db.getAgent(req.params.id);
    if (!agent) {
      return res.status(404).json({ error: 'Agent not found' });
    }
    const { token, ...sanitized } = agent;
    res.json({ agent: sanitized });
  } catch (error) {
    res.status(500).json({ error: 'Failed to fetch agent' });
  }
});

// Add new agent
router.post('/', async (req, res) => {
  try {
    const { token } = req.body;

    if (!token) {
      return res.status(400).json({ error: 'Token is required' });
    }

    // Validate token by fetching agent info
    const client = new SpaceTradersClient(API_BASE_URL, token);
    let agentData;

    try {
      agentData = await client.get('/my/agent');
    } catch (error) {
      return res.status(401).json({ error: 'Invalid agent token' });
    }

    const agent = await db.addAgent({
      token,
      symbol: agentData.data.symbol,
    });

    const { token: _, ...sanitized } = agent;
    res.status(201).json({ agent: sanitized });
  } catch (error: any) {
    if (error.message === 'Agent already exists') {
      return res.status(409).json({ error: error.message });
    }
    res.status(500).json({ error: 'Failed to add agent' });
  }
});

// Update agent (toggle visibility, etc.)
router.patch('/:id', async (req, res) => {
  try {
    const { visible, symbol, color } = req.body;
    const updates: any = {};

    if (typeof visible === 'boolean') updates.visible = visible;
    if (symbol) updates.symbol = symbol;
    if (color) updates.color = color;

    const agent = await db.updateAgent(req.params.id, updates);

    if (!agent) {
      return res.status(404).json({ error: 'Agent not found' });
    }

    const { token, ...sanitized } = agent;
    res.json({ agent: sanitized });
  } catch (error) {
    res.status(500).json({ error: 'Failed to update agent' });
  }
});

// Delete agent
router.delete('/:id', async (req, res) => {
  try {
    const deleted = await db.deleteAgent(req.params.id);

    if (!deleted) {
      return res.status(404).json({ error: 'Agent not found' });
    }

    res.status(204).send();
  } catch (error) {
    res.status(500).json({ error: 'Failed to delete agent' });
  }
});

// Get agent's ships
router.get('/:id/ships', async (req, res) => {
  try {
    const agent = await db.getAgent(req.params.id);
    if (!agent) {
      return res.status(404).json({ error: 'Agent not found' });
    }

    const client = new SpaceTradersClient(API_BASE_URL, agent.token);

    // Fetch all ships with pagination
    let allShips: any[] = [];
    let page = 1;
    let hasMore = true;
    const limit = 20;

    while (hasMore) {
      const response = await client.get(`/my/ships?page=${page}&limit=${limit}`);
      allShips = [...allShips, ...response.data];

      // Check if there are more pages
      if (response.meta?.page && response.meta?.limit && response.meta?.total) {
        if (response.meta.page * response.meta.limit >= response.meta.total) {
          hasMore = false;
        } else {
          page++;
        }
      } else if (response.data.length < limit) {
        hasMore = false;
      } else {
        page++;
      }
    }

    res.json({ data: allShips, meta: { total: allShips.length } });
  } catch (error) {
    console.error('Failed to fetch ships:', error);
    res.status(500).json({ error: 'Failed to fetch ships' });
  }
});

export default router;
