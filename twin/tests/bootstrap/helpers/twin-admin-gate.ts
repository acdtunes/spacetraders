import { TWIN_ADMIN } from '../../helpers/run-cli';
import { twin, type TwinState } from './twin-admin';
import type { GateFixture } from './gate-fixtures';

export interface GateWorker { symbol: string; source: 'repurposed' | 'bought' }
export interface GateState extends TwinState {
  construction: { site: string; percent: number; started: boolean; adopted: boolean };
  gateWorkers: GateWorker[];
  executorRunning: boolean;
  autosizerRunning: boolean;
  standingCoordinators: { siting: boolean; workerRebalancer: boolean };
  done: boolean;
}

async function post(pathUnder: string, body: unknown): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}${pathUnder}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST /_twin${pathUnder} → ${res.status} ${await res.text()}`);
}

export const twinGate = {
  ...twin,
  async seedGate(fixture: GateFixture): Promise<void> {
    await post('/reset', { mode: 'gate-entry', ...fixture });
  },
  async setConstruction(percent: number): Promise<void> {
    await post('/construction', { percent });
  },
  async gateState(): Promise<GateState> {
    const res = await fetch(`${TWIN_ADMIN}/state`);
    if (!res.ok) throw new Error(`GET /_twin/state → ${res.status}`);
    return res.json() as Promise<GateState>;
  },
};
