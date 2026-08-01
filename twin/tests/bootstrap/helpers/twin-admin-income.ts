import { TWIN_ADMIN } from '../../helpers/run-cli';
import { twin, type TwinState } from './twin-admin';
import type { IncomeFixture } from './fixtures-income';

export interface IncomeHauler { symbol: string; role: string; parkedHub: string | null }
export interface IncomeState extends TwinState {
  haulers: IncomeHauler[];
  frigateContractTagged: boolean;
  batchContractRunning: boolean;
  creditsPerHour: number;
  hubs: string[];
}

async function post(pathUnder: string, body: unknown): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}${pathUnder}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`POST /_twin${pathUnder} → ${res.status} ${await res.text()}`);
}

export const twinIncome = {
  ...twin, // reuse reset/state/clock/setCredits/forceCoverage/injectFault
  async seedIncome(fixture: IncomeFixture): Promise<void> {
    await post('/reset', { mode: 'income-entry', ...fixture });
  },
  async setIncome(creditsPerHour: number): Promise<void> {
    await post('/income', { creditsPerHour });
  },
  async incomeState(): Promise<IncomeState> {
    const res = await fetch(`${TWIN_ADMIN}/state`);
    if (!res.ok) throw new Error(`GET /_twin/state → ${res.status}`);
    return res.json() as Promise<IncomeState>;
  },
};
