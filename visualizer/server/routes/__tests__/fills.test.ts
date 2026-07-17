import { describe, it, expect } from 'vitest';
import { mergeFills } from '../../utils/fills.js';

describe('mergeFills', () => {
  const tele = [
    { id: 11, ship_symbol: 'SHIP-1', good: 'IRON', is_buy: true, realized_units: 40, realized_unit_price: 30, waypoint: 'X1-AA-P1', realized_at: '2026-07-17T12:00:00Z' },
    { id: 12, ship_symbol: 'SHIP-1', good: 'IRON', is_buy: false, realized_units: 40, realized_unit_price: 55, waypoint: 'X1-BB-Q2', realized_at: '2026-07-17T12:10:00Z' },
  ];
  const arb = [
    { id: 7, ship_symbol: 'SHIP-2', good_symbol: 'FUEL', units_sold: 20, actual_net_profit: 900, sell_market: 'X1-CC-R3', executed_at: '2026-07-17T12:05:00Z' },
  ];

  it('merges both sources desc by time with stable ids and signed credits', () => {
    const fills = mergeFills(tele, arb, 30);
    expect(fills.map((f) => f.id)).toEqual(['t-12', 'a-7', 't-11']);
    expect(fills[0]).toMatchObject({ ship: 'SHIP-1', good: 'IRON', isBuy: false, units: 40, credits: 2200, waypoint: 'X1-BB-Q2' });
    expect(fills[1]).toMatchObject({ ship: 'SHIP-2', good: 'FUEL', isBuy: false, credits: 900, waypoint: 'X1-CC-R3' });
    expect(fills[2].credits).toBe(-1200); // buy: negative
  });

  it('applies the limit after merging and skips malformed rows', () => {
    expect(mergeFills(tele, arb, 2)).toHaveLength(2);
    expect(mergeFills([{ id: 1 } as any], [], 10)).toEqual([]);
  });
});
