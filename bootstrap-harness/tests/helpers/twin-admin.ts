import { ADMIN_URL } from './config';
import type { ResetFixture } from './fixtures';
import type { MutationLogEntry } from './mutation-log';

// Client for the API server's admin namespace (/_twin at runtime). This is HTTP only — it depends
// on no other package's source; ADMIN_URL is a runtime string from ./config.

export interface TwinShip {
  symbol: string;
  role: string;
  // GET /_twin/state serves the resolveNav'd `route` (departureTime + arrival) on any ship that has
  // a live/settled transit — absent/null for a ship that never moved. Restart mid-transit specs read
  // `nav.route.arrival` to prove the rebooted daemon ADOPTED the in-flight transit (same arrival
  // instant) rather than superseding it with a fresh navigate (which would re-mint a later arrival).
  nav: { status: string; waypoint: string; route?: { departureTime: string; arrival: string } | null };
  scoutAssignment: string | null;
}
export interface TwinState {
  agent: { credits: number };
  ships: TwinShip[];
  coverage: number;
  markets: { waypoint: string; scouted: boolean; fresh: boolean }[];
  clock: { now: string; mode: string };
  mutationLog: MutationLogEntry[];
}

async function post<T = unknown>(pathUnder: string, body?: unknown): Promise<T> {
  const res = await fetch(`${ADMIN_URL}${pathUnder}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${pathUnder} → ${res.status} ${await res.text()}`);
  return (res.headers.get('content-type')?.includes('json') ? res.json() : undefined) as T;
}

export const twin = {
  async reset(fixture: ResetFixture = {}): Promise<void> {
    await post('/reset', fixture);
  },
  async state(): Promise<TwinState> {
    const res = await fetch(`${ADMIN_URL}/state`);
    if (!res.ok) throw new Error(`GET /state → ${res.status}`);
    return res.json() as Promise<TwinState>;
  },
  clock(opts: { mode?: 'frozen' | 'running'; advanceMs?: number; setNow?: string }) {
    return post<{ now: string }>('/clock', opts);
  },
  // Live-retune the twin's ONE travel-compression knob (routes/admin.ts POST /_twin/time-compression):
  // arrivalMs = realETA / factor. A LOWER factor => slower travel => a WIDER IN_TRANSIT window, which
  // lets a restart mid-transit spec deterministically catch a ship in flight at the orchestrator's
  // fast default (200x makes a >=15s real leg resolve in ~50-300ms — too fast to catch). CAUTION:
  // compression is STICKY across POST /_twin/reset, so any spec that lowers it MUST restore the fast
  // default (finally) or every later spec inherits the slow clock.
  setCompression(factor: number) {
    return post<{ compression: number }>('/time-compression', { compression: factor });
  },
  async setCredits(credits: number): Promise<void> {
    await post('/agent', { credits });
  },
  forceCoverage(opts: { fraction?: number; scoutWaypoints?: string[] }) {
    return post<{ coverage: number }>('/markets/coverage', opts);
  },
  async injectFault(opts: { endpoint: string; code: number; count: number }): Promise<void> {
    await post('/fault', opts);
  },
};
