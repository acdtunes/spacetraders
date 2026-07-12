import { describe, expect, it } from 'vitest';
import { countCall, ticksOf, type MutationLogEntry } from '../helpers/mutation-log';

const LOG: MutationLogEntry[] = [
  { seq: 1, call: 'PurchaseShip', detail: { shipType: 'SHIP_PROBE' }, at: '2026-01-01T00:00:01Z' },
  { seq: 2, call: 'navigate', at: '2026-01-01T00:00:02Z' },
  { seq: 3, call: 'PurchaseShip', detail: { shipType: 'SHIP_PROBE' }, at: '2026-01-01T00:00:06Z' },
];

describe('mutation-log queries', () => {
  it('counts calls by name', () => {
    expect(countCall(LOG, 'PurchaseShip')).toBe(2);
    expect(countCall(LOG, 'refuel')).toBe(0);
  });
  it('returns the world-times of matching calls', () => {
    expect(ticksOf(LOG, 'PurchaseShip')).toEqual(['2026-01-01T00:00:01Z', '2026-01-01T00:00:06Z']);
  });
});
