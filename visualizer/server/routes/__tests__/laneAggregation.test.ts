import { describe, it, expect } from 'vitest';
import { aggregateLanes } from '../../utils/laneAggregation.js';

const t = (leg: number, wp: string, isBuy: boolean, units: number, price: number, atIso: string) => ({
  tourId: 'tour-1', shipSymbol: 'SHIP-1', legIndex: leg, waypoint: wp,
  isBuy, realizedUnits: units, realizedUnitPrice: price, realizedAt: atIso, good: 'ORE',
});

const W_START = Date.parse('2026-07-10T00:00:00Z');
const W_END = Date.parse('2026-07-10T06:00:00Z');
const inWin = '2026-07-10T03:00:00Z';

describe('aggregateLanes', () => {
  it('pairs consecutive legs into directed lanes with signed profit (sell +, buy -)', () => {
    const lanes = aggregateLanes([
      t(0, 'X1-A-1', true, 100, 50, inWin),   // buy 5000 at leg 0
      t(1, 'X1-A-2', false, 100, 80, inWin),  // sell 8000 at leg 1
    ], [], W_START, W_END);
    // Lane A-1 -> A-2 realizes the leg-1 (destination) value: +8000.
    expect(lanes).toHaveLength(1);
    expect(lanes[0]).toMatchObject({ from: 'X1-A-1', to: 'X1-A-2', realizedProfit: 8000, realizedUnits: 100 });
  });

  it('excludes rows outside the window (both edges)', () => {
    const before = '2026-07-09T23:59:59Z';
    const after = '2026-07-10T06:00:01Z';
    const lanes = aggregateLanes([
      t(0, 'X1-A-1', false, 10, 10, before),
      t(1, 'X1-A-2', false, 10, 10, after),
    ], [], W_START, W_END);
    expect(lanes).toEqual([]);
  });

  it('folds arb executions into one-hop lanes and can yield a net loss', () => {
    const lanes = aggregateLanes([], [
      { buyMarket: 'X1-B-1', sellMarket: 'X1-B-2', unitsSold: 40, actualNetProfit: -1200, executedAt: inWin, goodSymbol: 'ORE' },
    ], W_START, W_END);
    expect(lanes).toHaveLength(1);
    expect(lanes[0]).toMatchObject({ from: 'X1-B-1', to: 'X1-B-2', realizedProfit: -1200 });
  });

  it('sorts lanes by realized profit descending', () => {
    const lanes = aggregateLanes([], [
      { buyMarket: 'X1-A', sellMarket: 'X1-B', unitsSold: 1, actualNetProfit: 100, executedAt: inWin, goodSymbol: 'ORE' },
      { buyMarket: 'X1-C', sellMarket: 'X1-D', unitsSold: 1, actualNetProfit: 900, executedAt: inWin, goodSymbol: 'ORE' },
    ], W_START, W_END);
    expect(lanes.map((l) => l.realizedProfit)).toEqual([900, 100]);
  });

  it('ignores self-loops (same waypoint / same market)', () => {
    const lanes = aggregateLanes(
      [t(0, 'X1-A-1', false, 5, 5, inWin), t(1, 'X1-A-1', false, 5, 5, inWin)],
      [{ buyMarket: 'X1-Z', sellMarket: 'X1-Z', unitsSold: 5, actualNetProfit: 10, executedAt: inWin, goodSymbol: 'ORE' }],
      W_START, W_END,
    );
    expect(lanes).toEqual([]);
  });
});

import { rollupSystemLanes, rollupSystemActivity, systemOfWaypoint } from '../../utils/laneAggregation.js';

describe('system rollups', () => {
  const lanes = [
    { from: 'X1-AA-P1', to: 'X1-BB-Q2', realizedUnits: 100, realizedProfit: 50000, legCount: 2, goods: {} },
    { from: 'X1-AA-P9', to: 'X1-BB-Q2', realizedUnits: 40, realizedProfit: 10000, legCount: 1, goods: {} },
    { from: 'X1-AA-P1', to: 'X1-AA-P2', realizedUnits: 60, realizedProfit: 7000, legCount: 3, goods: {} },
    { from: 'X1-BB-Q2', to: 'X1-AA-P1', realizedUnits: 10, realizedProfit: -500, legCount: 1, goods: {} },
  ];

  it('systemOfWaypoint truncates to SECTOR-SYSTEM', () => {
    expect(systemOfWaypoint('X1-AA-P1')).toBe('X1-AA');
    expect(systemOfWaypoint('WEIRD')).toBe('WEIRD');
  });

  it('rolls waypoint lanes up to directed system lanes, dropping intra-system', () => {
    const sys = rollupSystemLanes(lanes);
    // goods/topGoods are additive; these inputs carry no goods, so both are empty.
    expect(sys).toEqual([
      { from: 'X1-AA', to: 'X1-BB', realizedUnits: 140, realizedProfit: 60000, legCount: 3, goods: {}, topGoods: [] },
      { from: 'X1-BB', to: 'X1-AA', realizedUnits: 10, realizedProfit: -500, legCount: 1, goods: {}, topGoods: [] },
    ]);
  });

  it('credits activity to the destination system (intra credits its own)', () => {
    const act = rollupSystemActivity(lanes);
    expect(act).toEqual([
      { system: 'X1-BB', realizedProfit: 60000, legCount: 3 },
      { system: 'X1-AA', realizedProfit: 6500, legCount: 4 },
    ]);
  });
});

describe('goods accumulation + topGoods', () => {
  it('waypoint lanes accumulate per-good signed credits', () => {
    const tele = [
      { tourId: 'T', shipSymbol: 'S', legIndex: 0, waypoint: 'X1-AA-P1', isBuy: true, realizedUnits: 10, realizedUnitPrice: 100, realizedAt: '2026-07-17T12:00:00Z', good: 'IRON' },
      { tourId: 'T', shipSymbol: 'S', legIndex: 1, waypoint: 'X1-BB-Q2', isBuy: false, realizedUnits: 10, realizedUnitPrice: 180, realizedAt: '2026-07-17T12:05:00Z', good: 'IRON' },
    ];
    const lanes = aggregateLanes(tele as any, [], Date.parse('2026-07-17T11:00:00Z'), Date.parse('2026-07-17T13:00:00Z'));
    expect(lanes[0].goods).toEqual({ IRON: 1800 });
  });

  it('system rollup merges goods and emits top-3 topGoods by |credits|', () => {
    const lanes = [
      { from: 'X1-AA-P1', to: 'X1-BB-Q2', realizedUnits: 1, realizedProfit: 100, legCount: 1, goods: { IRON: 900, GOLD: -50 } },
      { from: 'X1-AA-P9', to: 'X1-BB-Q3', realizedUnits: 1, realizedProfit: 100, legCount: 1, goods: { IRON: 100, FUEL: 300, ALUM: 10, COPPER: 5 } },
    ];
    const sys = rollupSystemLanes(lanes as any);
    expect(sys[0].topGoods).toEqual([
      { good: 'IRON', credits: 1000 },
      { good: 'FUEL', credits: 300 },
      { good: 'GOLD', credits: -50 },
    ]);
  });
});
