import type { World } from './types.js';
import { loadColdStartWorld, registerAgent } from './loader.js';

/** The single in-memory world every route reads/mutates. Lazily built on first access. */
let current: World | null = null;

export function getWorld(): World {
  if (current === null) current = loadColdStartWorld();
  return current;
}

/** Replace the live world outright (buildServer({ world }) and tests). */
export function setWorld(world: World): void { current = world; }

/** POST /_twin/reset: rebuild cold-start from fixtures, PRESERVING the registered
 *  agent's symbol/faction/token so the seeded players row stays valid. Replaces the
 *  singleton — safe because no route captures a reference (all call getWorld()). */
export function resetWorld(): void {
  const prev = getWorld();
  const prevSymbol = prev.agent?.symbol ?? null;
  const prevFaction = prev.agent?.startingFaction ?? null;
  const prevToken = prev.agentToken;

  const fresh = loadColdStartWorld();
  if (prevSymbol !== null && prevToken !== null) {
    registerAgent(fresh, { symbol: prevSymbol, faction: prevFaction ?? 'COSMIC', token: prevToken });
  }
  current = fresh;
}
