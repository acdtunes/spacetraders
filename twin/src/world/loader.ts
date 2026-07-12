// twin/src/world/loader.ts — the single world module for the digital twin.
// Materializes the captured X1-PZ28 snapshot into the foundation world types, and
// (mintToken/registerAgent, added in Tasks 9/10) mints the cold-start agent on top.
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Agent, Market, Ship, Shipyard, System, TransitState, Waypoint, World } from './types.js';

const MODULE_DIR = path.dirname(fileURLToPath(import.meta.url));

/** Absolute path to the checked-in captured home-system fixture directory. */
export const FIXTURES_DIR = path.resolve(MODULE_DIR, '../../fixtures/era2-X1-PZ28');

/** The pristine POST /register payload template (fixtures/era2-X1-PZ28/register.json).
 *  Ship symbols carry the "{AGENT}" placeholder registerAgent substitutes. */
export interface RegisterTemplate {
  startingCredits: number;
  headquarters: string;
  startingFaction: string;
  ships: Ship[];
}

function readJson<T>(dir: string, file: string): T {
  return JSON.parse(readFileSync(path.join(dir, file), 'utf8')) as T;
}

export function loadRegisterTemplate(dir: string = FIXTURES_DIR): RegisterTemplate {
  return readJson<RegisterTemplate>(dir, 'register.json');
}

/** Load the PRE-register captured world: serverStatus/systems/markets/shipyards from the
 *  capture; agent/agentToken null; ships/transits empty; shipCounter 0. */
export function loadColdStartWorld(dir: string = FIXTURES_DIR): World {
  const serverStatus = readJson<World['serverStatus']>(dir, 'server-status.json');
  const waypoints = readJson<Waypoint[]>(dir, 'waypoints.json');
  const markets = readJson<Market[]>(dir, 'markets.json');
  const shipyards = readJson<Shipyard[]>(dir, 'shipyards.json');

  const systems = new Map<string, System>();
  for (const wp of waypoints) {
    let system = systems.get(wp.systemSymbol);
    if (!system) {
      system = { symbol: wp.systemSymbol, waypoints: new Map<string, Waypoint>() };
      systems.set(wp.systemSymbol, system);
    }
    system.waypoints.set(wp.symbol, wp);
  }

  const marketMap = new Map<string, Market>();
  for (const m of markets) marketMap.set(m.symbol, m);
  const shipyardMap = new Map<string, Shipyard>();
  for (const sy of shipyards) shipyardMap.set(sy.symbol, sy);

  return {
    serverStatus,
    agent: null,
    agentToken: null,
    ships: new Map<string, Ship>(),
    systems,
    markets: marketMap,
    shipyards: shipyardMap,
    transits: new Map<string, TransitState>(),
    shipCounter: 0,
  };
}

/** base64url (RFC 4648 §5) with padding stripped. */
function b64url(s: string): string {
  return Buffer.from(s, 'utf8').toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** Mint the twin's opaque, JWT-shaped agent token. DETERMINISTIC per symbol: the Go
 *  client never decodes it (only ever a Bearer string), and determinism lets reset
 *  reissue the identical token and tests reproduce players.token from the symbol. */
export function mintToken(symbol: string): string {
  const header = b64url(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const payload = b64url(JSON.stringify({ identifier: symbol, version: 'twin' }));
  const signature = b64url(`twin-signature.${symbol}`);
  return `${header}.${payload}.${signature}`;
}

/** Materialize the cold-start agent + starting ships from register.json into `world`
 *  using the PROVIDED token, and return the /register response data. Clears transits
 *  (cold start ⇒ all ships DOCKED) and sets shipCounter = ships.length + 1. Also used
 *  by POST /_twin/reset with the preserved token. */
export function registerAgent(
  world: World,
  args: { symbol: string; faction: string; token: string },
  template: RegisterTemplate = loadRegisterTemplate(),
): { agent: Agent; ships: Ship[] } {
  const { symbol, token } = args;
  const faction = args.faction || template.startingFaction;

  const agent: Agent = {
    accountId: `twin-account-${symbol}`,
    symbol,
    headquarters: template.headquarters,
    credits: template.startingCredits,
    startingFaction: faction,
  };

  // Deep-replace the "{AGENT}" placeholder (JSON round-trip also deep-clones the
  // shared template so it is never mutated).
  const ships = JSON.parse(JSON.stringify(template.ships).split('{AGENT}').join(symbol)) as Ship[];

  world.agent = agent;
  world.agentToken = token;
  world.ships = new Map(ships.map((s) => [s.symbol, s]));
  world.transits = new Map();
  world.shipCounter = ships.length + 1;

  return { agent, ships };
}
