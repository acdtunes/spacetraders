import { ADMIN_URL } from './config';
import { twin, type TwinState } from './twin-admin';
import type { GateFixture } from './fixtures-gate';

export interface GateWorker {
  symbol: string;
  source: 'repurposed' | 'bought';
}
export interface GateState extends TwinState {
  construction: { site: string; percent: number; started: boolean; adopted: boolean };
  gateWorkers: GateWorker[];
  executorRunning: boolean;
  autosizerRunning: boolean;
  standingCoordinators: { siting: boolean; workerRebalancer: boolean };
  done: boolean;
}

async function post(pathUnder: string, body: unknown): Promise<void> {
  const res = await fetch(`${ADMIN_URL}${pathUnder}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST ${pathUnder} → ${res.status} ${await res.text()}`);
}

export const twinGate = {
  ...twin, // reuse reset/state/clock/setCredits/forceCoverage/injectFault
  async seedGate(fixture: GateFixture): Promise<void> {
    await post('/reset', { mode: 'gate-entry', ...fixture });
  },
  async setConstruction(percent: number): Promise<void> {
    await post('/construction', { percent });
  },
  async gateState(): Promise<GateState> {
    const res = await fetch(`${ADMIN_URL}/state`);
    if (!res.ok) throw new Error(`GET /state → ${res.status}`);
    return res.json() as Promise<GateState>;
  },
};
