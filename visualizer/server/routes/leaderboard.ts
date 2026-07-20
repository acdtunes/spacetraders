import { Router } from 'express';
import { SpaceTradersClient } from '../src/client.js';

const router = Router();
const API_BASE_URL = 'https://api.spacetraders.io/v2';

// GET /api/leaderboard
// Proxies the public SpaceTraders global status endpoint (GET /v2/) and returns
// just the leaderboard payload. No agent token required — this is public data.
router.get('/', async (_req, res) => {
  try {
    const client = new SpaceTradersClient(API_BASE_URL);
    const status = await client.get('/');
    res.json({
      resetDate: status.resetDate ?? null,
      leaderboards: status.leaderboards ?? { mostCredits: [], mostSubmittedCharts: [] },
    });
  } catch (error) {
    console.error('Failed to fetch leaderboard:', error);
    res.status(500).json({ error: 'Failed to fetch leaderboard' });
  }
});

export default router;
