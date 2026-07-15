#!/usr/bin/env node

import express from 'express';
import cors from 'cors';
import agentsRouter from './routes/agents.js';
import systemsRouter from './routes/systems.js';
import flowsRouter from './routes/flows.js';
import contractOpsRouter from './routes/contract-ops.js';

const app = express();
const PORT = process.env.PORT || 4000;

// Middleware
app.use(cors());
app.use(express.json());

// Routes
app.use('/api/agents', agentsRouter);
app.use('/api/systems', systemsRouter);
app.use('/api/flows', flowsRouter);
app.use('/api/contract-ops', contractOpsRouter);

// Optional bot routes (requires PostgreSQL)
try {
  const { default: botRouter } = await import('./routes/bot.js');
  app.use('/api/bot', botRouter);
  // Importing bot.js only constructs the pg Pool (lazy) — it does NOT open a
  // connection, so this is not proof PostgreSQL is up. Routes mount regardless;
  // a down DB surfaces as a 503 from the endpoints, not a 404. Only a module-load
  // failure lands in the catch below.
  console.log('✓ Bot routes mounted (PostgreSQL connection is lazy/unchecked)');
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
