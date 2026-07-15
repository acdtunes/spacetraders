import { describe, it, expect } from 'vitest';
import {
  parseElements,
  parseDeliveries,
  reduceWarehouseLevels,
  derivePhase,
  computeCycleStats,
  classifyShip,
  systemOf,
  involvedSystems,
  mergeEvents,
} from '../../utils/contractOps.js';

describe('parseElements', () => {
  it('parses depot element arrays with PascalCase keys', () => {
    const json = '[{"Waypoint":"X1-VB74-J58","ShipSymbol":"TORWIND-11"},{"Waypoint":"X1-VB74-E45","ShipSymbol":""}]';
    expect(parseElements(json)).toEqual([
      { waypoint: 'X1-VB74-J58', shipSymbol: 'TORWIND-11' },
      { waypoint: 'X1-VB74-E45', shipSymbol: '' },
    ]);
  });

  it('returns [] for null, empty, or malformed input', () => {
    expect(parseElements(null)).toEqual([]);
    expect(parseElements('')).toEqual([]);
    expect(parseElements('not-json')).toEqual([]);
    expect(parseElements('{"Waypoint":"x"}')).toEqual([]); // not an array
  });
});

describe('parseDeliveries', () => {
  it('parses deliveries_json PascalCase into camelCase progress', () => {
    const json = '[{"TradeSymbol":"COPPER_ORE","DestinationSymbol":"X1-VB74-H51","UnitsRequired":168,"UnitsFulfilled":80}]';
    expect(parseDeliveries(json)).toEqual([
      { tradeSymbol: 'COPPER_ORE', destinationSymbol: 'X1-VB74-H51', unitsRequired: 168, unitsFulfilled: 80 },
    ]);
  });

  it('returns [] for null or malformed input', () => {
    expect(parseDeliveries(null)).toEqual([]);
    expect(parseDeliveries('nope')).toEqual([]);
  });
});

describe('reduceWarehouseLevels', () => {
  it('nets stockings minus withdrawals per waypoint and good', () => {
    const levels = reduceWarehouseLevels(
      [
        { waypoint: 'X1-A', good: 'COPPER_ORE', units: 120 },
        { waypoint: 'X1-A', good: 'COPPER_ORE', units: 40 },
        { waypoint: 'X1-A', good: 'IRON', units: 10 },
        { waypoint: 'X1-B', good: 'COPPER_ORE', units: 5 },
      ],
      [
        { waypoint: 'X1-A', good: 'COPPER_ORE', units: 60 },
      ],
    );
    expect(levels).toContainEqual({ waypoint: 'X1-A', good: 'COPPER_ORE', units: 100 });
    expect(levels).toContainEqual({ waypoint: 'X1-A', good: 'IRON', units: 10 });
    expect(levels).toContainEqual({ waypoint: 'X1-B', good: 'COPPER_ORE', units: 5 });
  });

  it('clamps negative nets to zero and drops zero rows', () => {
    const levels = reduceWarehouseLevels(
      [{ waypoint: 'X1-A', good: 'GOLD', units: 5 }],
      [
        { waypoint: 'X1-A', good: 'GOLD', units: 9 },
        { waypoint: 'X1-C', good: 'ICE', units: 3 },
      ],
    );
    expect(levels).toEqual([]);
  });

  it('sorts by units descending', () => {
    const levels = reduceWarehouseLevels(
      [
        { waypoint: 'X1-A', good: 'SMALL', units: 1 },
        { waypoint: 'X1-A', good: 'BIG', units: 100 },
      ],
      [],
    );
    expect(levels[0].good).toBe('BIG');
  });
});

describe('derivePhase', () => {
  const delivery = (fulfilled: number, required = 100, good = 'COPPER_ORE') => ({
    tradeSymbol: good, destinationSymbol: 'X1-D', unitsRequired: required, unitsFulfilled: fulfilled,
  });

  it('is IDLE with no contract and no running worker', () => {
    expect(derivePhase({ contract: null, workerRunning: false, workerCargo: [] })).toBe('IDLE');
  });

  it('is NEGOTIATE with no contract but a running worker', () => {
    expect(derivePhase({ contract: null, workerRunning: true, workerCargo: [] })).toBe('NEGOTIATE');
  });

  it('is ACCEPT when the contract exists but is not accepted', () => {
    expect(derivePhase({
      contract: { accepted: false, deliveries: [delivery(0)] },
      workerRunning: true,
      workerCargo: [],
    })).toBe('ACCEPT');
  });

  it('is SOURCE when accepted, units remain, and the worker holds none of the good', () => {
    expect(derivePhase({
      contract: { accepted: true, deliveries: [delivery(20)] },
      workerRunning: true,
      workerCargo: [{ symbol: 'FUEL', units: 10 }],
    })).toBe('SOURCE');
  });

  it('is DELIVER when accepted, units remain, and the worker holds the contract good', () => {
    expect(derivePhase({
      contract: { accepted: true, deliveries: [delivery(20)] },
      workerRunning: true,
      workerCargo: [{ symbol: 'COPPER_ORE', units: 40 }],
    })).toBe('DELIVER');
  });

  it('is FULFILL when every delivery is complete', () => {
    expect(derivePhase({
      contract: { accepted: true, deliveries: [delivery(100)] },
      workerRunning: true,
      workerCargo: [],
    })).toBe('FULFILL');
  });
});

describe('computeCycleStats', () => {
  const now = Date.parse('2026-07-15T12:00:00Z');
  const iso = (minAgo: number) => new Date(now - minAgo * 60_000).toISOString();

  it('counts contracts fulfilled in the last hour and averages consecutive gaps', () => {
    const stats = computeCycleStats(
      [
        { fulfilled: true, lastUpdated: iso(10) },
        { fulfilled: true, lastUpdated: iso(30) },
        { fulfilled: true, lastUpdated: iso(50) },
        { fulfilled: true, lastUpdated: iso(90) }, // outside the hour, still feeds the avg
        { fulfilled: false, lastUpdated: iso(1) }, // unfulfilled rows ignored
      ],
      now,
    );
    expect(stats.fulfilledLastHour).toBe(3);
    // gaps: 20, 20, 40 minutes → avg ≈ 26.7
    expect(stats.avgCycleMinutes).toBeCloseTo(26.7, 0);
  });

  it('returns null avg with fewer than two fulfilled rows', () => {
    const stats = computeCycleStats([{ fulfilled: true, lastUpdated: iso(5) }], now);
    expect(stats.fulfilledLastHour).toBe(1);
    expect(stats.avgCycleMinutes).toBeNull();
  });
});

describe('classifyShip', () => {
  const containers = new Map([
    ['stocker-1', { containerType: 'TRADING', commandType: 'stocker' }],
    ['wh-1', { containerType: 'WAREHOUSE', commandType: 'warehouse' }],
    ['work-1', { containerType: 'CONTRACT_WORKFLOW', commandType: 'contract_workflow' }],
  ]);
  const depotSets = {
    delivery: new Set(['TORWIND-15']),
    warehouse: new Set(['TORWIND-11']),
    stocker: new Set(['TORWIND-13']),
  };

  it('prefers the live container identity over the standing tag', () => {
    expect(classifyShip({ shipSymbol: 'S', dedicatedFleet: 'contract', containerId: 'work-1' }, containers, depotSets)).toBe('worker');
    expect(classifyShip({ shipSymbol: 'S', dedicatedFleet: 'contract', containerId: 'stocker-1' }, containers, depotSets)).toBe('stocker');
    expect(classifyShip({ shipSymbol: 'S', dedicatedFleet: '', containerId: 'wh-1' }, containers, depotSets)).toBe('warehouse');
  });

  it('falls back to the standing dedicated_fleet tag', () => {
    expect(classifyShip({ shipSymbol: 'S', dedicatedFleet: 'stocker', containerId: null }, containers, depotSets)).toBe('stocker');
    expect(classifyShip({ shipSymbol: 'S', dedicatedFleet: 'warehouse', containerId: null }, containers, depotSets)).toBe('warehouse');
  });

  it('classifies untagged, uncontainered hulls by their depot pins (this era leaves dedicated_fleet empty)', () => {
    expect(classifyShip({ shipSymbol: 'TORWIND-11', dedicatedFleet: '', containerId: null }, containers, depotSets)).toBe('warehouse');
    expect(classifyShip({ shipSymbol: 'TORWIND-13', dedicatedFleet: '', containerId: null }, containers, depotSets)).toBe('stocker');
    expect(classifyShip({ shipSymbol: 'TORWIND-15', dedicatedFleet: '', containerId: null }, containers, depotSets)).toBe('delivery');
  });

  it('marks depot-pinned delivery hulls even when fleet-tagged, else generic contract fleet', () => {
    expect(classifyShip({ shipSymbol: 'TORWIND-15', dedicatedFleet: 'contract', containerId: null }, containers, depotSets)).toBe('delivery');
    expect(classifyShip({ shipSymbol: 'TORWIND-99', dedicatedFleet: 'contract', containerId: null }, containers, depotSets)).toBe('contract');
  });
});

describe('systemOf / involvedSystems', () => {
  it('derives the system from a waypoint symbol', () => {
    expect(systemOf('X1-VB74-J58')).toBe('X1-VB74');
    expect(systemOf('X1-KA42-H51')).toBe('X1-KA42');
    expect(systemOf('MALFORMED')).toBeNull();
  });

  it('collects unique systems from depots and the contract destination', () => {
    const systems = involvedSystems(
      [
        { waypoint: 'X1-VB74-J58', shipSymbol: 'A' },
        { waypoint: 'X1-VB74-A1', shipSymbol: 'B' },
        { waypoint: 'X1-KN67-EZ2A', shipSymbol: 'C' },
      ],
      ['X1-VB74-H51'],
    );
    expect(systems.sort()).toEqual(['X1-KN67', 'X1-VB74']);
  });
});

describe('mergeEvents', () => {
  it('merges the three streams sorted newest-first and caps the count', () => {
    const events = mergeEvents(
      [{ at: '2026-07-15T10:00:00Z', good: 'ORE', units: 10, waypoint: 'X1-A', shipSymbol: 'S1' }],
      [{ at: '2026-07-15T11:00:00Z', good: 'ORE', units: 5, waypoint: 'X1-A', shipSymbol: 'S2', contractId: 'c1' }],
      [{ at: '2026-07-15T10:30:00Z', amount: 13121, description: 'Contract fulfilled' }],
      2,
    );
    expect(events).toHaveLength(2);
    expect(events[0].kind).toBe('withdrawal');
    expect(events[1].kind).toBe('transaction');
  });
});
