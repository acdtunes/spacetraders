import fs from 'fs/promises';
import path from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const DB_FILE = path.join(__dirname, 'agents.json');

export interface Agent {
  id: string;
  token: string;
  symbol: string;
  color: string;
  visible: boolean;
  createdAt: string;
}

interface Database {
  agents: Agent[];
}

const AGENT_COLORS = [
  '#FF6B6B', '#4ECDC4', '#95E1D3', '#FFE66D', '#A8E6CF',
  '#FF8B94', '#C7CEEA', '#FFEAA7', '#DFE6E9', '#74B9FF'
];

// Initialize database if it doesn't exist
async function initDb(): Promise<Database> {
  try {
    const data = await fs.readFile(DB_FILE, 'utf-8');
    return JSON.parse(data);
  } catch (error) {
    // File doesn't exist, create it
    const initialDb: Database = { agents: [] };
    await fs.writeFile(DB_FILE, JSON.stringify(initialDb, null, 2));
    return initialDb;
  }
}

async function readDb(): Promise<Database> {
  try {
    const data = await fs.readFile(DB_FILE, 'utf-8');
    return JSON.parse(data);
  } catch (error) {
    return await initDb();
  }
}

async function writeDb(db: Database): Promise<void> {
  await fs.writeFile(DB_FILE, JSON.stringify(db, null, 2));
}

export async function getAllAgents(): Promise<Agent[]> {
  const db = await readDb();
  return db.agents;
}

export async function getAgent(id: string): Promise<Agent | null> {
  const db = await readDb();
  return db.agents.find(a => a.id === id) || null;
}

export async function addAgent(agent: Omit<Agent, 'id' | 'color' | 'visible' | 'createdAt'>): Promise<Agent> {
  const db = await readDb();

  // Check if agent already exists by token or symbol
  const existing = db.agents.find(a => a.token === agent.token || a.symbol === agent.symbol);
  if (existing) {
    throw new Error('Agent already exists');
  }

  const newAgent: Agent = {
    ...agent,
    id: crypto.randomUUID(),
    color: AGENT_COLORS[db.agents.length % AGENT_COLORS.length],
    visible: true,
    createdAt: new Date().toISOString(),
  };

  db.agents.push(newAgent);
  await writeDb(db);

  return newAgent;
}

export async function updateAgent(id: string, updates: Partial<Omit<Agent, 'id' | 'token' | 'createdAt'>>): Promise<Agent | null> {
  const db = await readDb();
  const index = db.agents.findIndex(a => a.id === id);

  if (index === -1) return null;

  db.agents[index] = { ...db.agents[index], ...updates };
  await writeDb(db);

  return db.agents[index];
}

export async function deleteAgent(id: string): Promise<boolean> {
  const db = await readDb();
  const initialLength = db.agents.length;
  db.agents = db.agents.filter(a => a.id !== id);

  if (db.agents.length !== initialLength) {
    await writeDb(db);
    return true;
  }

  return false;
}
