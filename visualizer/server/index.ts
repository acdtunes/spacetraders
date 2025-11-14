#!/usr/bin/env node

import express from 'express';
import cors from 'cors';
import agentsRouter from './routes/agents.js';
import systemsRouter from './routes/systems.js';

const app = express();
const PORT = process.env.PORT || 4000;

// Middleware
app.use(cors());
app.use(express.json());

// Routes
app.use('/api/agents', agentsRouter);
app.use('/api/systems', systemsRouter);

// Optional bot routes (requires PostgreSQL)
try {
  const { default: botRouter } = await import('./routes/bot.js');
  app.use('/api/bot', botRouter);
  console.log('✓ Bot routes enabled (PostgreSQL connected)');
} catch (error) {
  console.warn('⚠ Bot routes disabled (PostgreSQL not available)');
  console.warn('  To enable bot features, start PostgreSQL on port 5432');
  console.warn('  or set DATABASE_URL environment variable');
}

// Health check
app.get('/health', (req, res) => {
  res.json({ status: 'ok', timestamp: new Date().toISOString() });
});

// Error handler
app.use((err: any, req: express.Request, res: express.Response, next: express.NextFunction) => {
  console.error(err.stack);
  res.status(500).json({ error: 'Something went wrong!' });
});

app.listen(PORT, () => {
  console.log(`Server listening on port ${PORT}`);
});
