import Database from "better-sqlite3";
import path from "node:path";
import { fileURLToPath } from "node:url";

// ES module equivalent of __dirname
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

// Path to bot database (relative to mcp/api directory)
const DB_PATH = path.resolve(__dirname, "..", "..", "..", "bot", "var", "data", "sqlite", "spacetraders.db");

interface PlayerRow {
  player_id: number;
  agent_symbol: string;
  token: string;
  created_at: string;
  last_active: string | null;
}

/**
 * Get token for a player from the database.
 * @param playerId - Player ID to look up
 * @returns Token string or null if player not found
 */
export function getTokenForPlayer(playerId: number): string | null {
  let db: Database.Database | null = null;
  try {
    db = new Database(DB_PATH, { readonly: true });
    const stmt = db.prepare<[number], PlayerRow>("SELECT token FROM players WHERE player_id = ?");
    const row = stmt.get(playerId);
    return row ? row.token : null;
  } catch (error) {
    console.error(`Failed to fetch token for player ${playerId}:`, error);
    return null;
  } finally {
    if (db) {
      db.close();
    }
  }
}
