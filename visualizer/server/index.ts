#!/usr/bin/env node

import express from 'express';
import cors from 'cors';
import agentsRouter from './routes/agents.js';
import systemsRouter from './routes/systems.js';
import botRouter from './routes/bot.js';

const app = express();
const PORT = process.env.PORT || 4000;

// Middleware
app.use(cors());
app.use(express.json());

// Routes
app.use('/api/agents', agentsRouter);
app.use('/api/systems', systemsRouter);
app.use('/api/bot', botRouter);

// Health check
app.get('/health', (req, res) => {
  res.json({ status: 'ok', timestamp: new Date().toISOString() });
});

// Error handler
app.use((err: any, req: express.Request, res: express.Response, next: express.NextFunction) => {
  console.error(err.stack);
  res.status(500).json({ error: 'Something went wrong!' });
});

app.listen(PORT);
