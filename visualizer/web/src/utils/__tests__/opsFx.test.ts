import { describe, it, expect } from 'vitest';
import { diffLiveForFx } from '../opsFx';
import type { ContractOpsLive } from '../../types/contractOps';

const base = (over: Partial<ContractOpsLive>): ContractOpsLive => ({
  playerId: 3,
  generatedAt: '2026-07-15T04:00:00Z',
  phase: 'SOURCE',
  contract: null,
  lastFulfilled: null,
  cycle: { fulfilledLastHour: 0, avgCycleMinutes: null },
  coordinator: null,
  worker: null,
  ships: [],
  warehouses: [],
  destinations: [],
  pl: null,
  events: [],
  recentFulfillments: [],
  ...over,
});

const stocking = (at: string, units = 20) => ({
  kind: 'stocking' as const, at, good: 'COPPER_ORE', units, waypoint: 'X1-A', shipSymbol: 'S1',
});

const contract = (id: string, fulfilled: number) => ({
  id,
  accepted: true,
  fulfilled: false,
  deadline: '2026-07-22T00:00:00Z',
  paymentOnAccepted: 100,
  paymentOnFulfilled: 900,
  deliveries: [{ tradeSymbol: 'COPPER_ORE', destinationSymbol: 'X1-D', unitsRequired: 100, unitsFulfilled: fulfilled }],
  lastUpdated: '2026-07-15T03:59:00Z',
});

describe('diffLiveForFx', () => {
  it('emits nothing on the first poll (no prev)', () => {
    expect(diffLiveForFx(null, base({ events: [stocking('t1')] }), 1000)).toEqual([]);
  });

  it('emits a stocking ripple only for events not seen in the previous poll', () => {
    const prev = base({ events: [stocking('t1')] });
    const next = base({ events: [stocking('t2'), stocking('t1')] });
    const fx = diffLiveForFx(prev, next, 5000);
    expect(fx).toHaveLength(1);
    expect(fx[0]).toMatchObject({ kind: 'stocking', waypoint: 'X1-A', createdAtMs: 5000 });
    expect(fx[0].text).toContain('+20 COPPER_ORE');
  });

  it('emits a delivery burst at the destination when fulfilled units advance', () => {
    const prev = base({ contract: contract('c1', 40) });
    const next = base({ contract: contract('c1', 75) });
    const fx = diffLiveForFx(prev, next, 5000);
    expect(fx).toHaveLength(1);
    expect(fx[0]).toMatchObject({ kind: 'delivery', waypoint: 'X1-D' });
    expect(fx[0].text).toContain('+35');
  });

  it('emits a fulfillment burst when the active contract completes', () => {
    const prev = base({ contract: contract('c1', 100) });
    const next = base({
      contract: contract('c2', 0),
      lastFulfilled: { id: 'c1', at: '2026-07-15T04:00:01Z', payment: 900 },
    });
    const fx = diffLiveForFx(prev, next, 5000);
    expect(fx.some((f) => f.kind === 'fulfillment' && f.waypoint === 'X1-D' && f.text.includes('900'))).toBe(true);
  });

  it('does not re-emit fulfillment when nothing changed', () => {
    const state = base({ contract: contract('c1', 50) });
    expect(diffLiveForFx(state, state, 5000)).toEqual([]);
  });
});
