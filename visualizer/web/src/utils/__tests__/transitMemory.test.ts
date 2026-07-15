import { describe, it, expect } from 'vitest';
import {
  updateTransitMemory,
  positionShip,
  type OpsShipSnapshot,
  type ShipMemory,
} from '../transitMemory';

const ship = (over: Partial<OpsShipSnapshot>): OpsShipSnapshot => ({
  symbol: 'TORWIND-6',
  navStatus: 'IN_ORBIT',
  waypoint: 'X1-VB74-A1',
  x: 0,
  y: 0,
  arrivalTime: null,
  ...over,
});

describe('updateTransitMemory', () => {
  it('records the last stationary position and clears any transit', () => {
    const prev = new Map<string, ShipMemory>([
      ['TORWIND-6', { transit: { originWaypoint: 'X', originX: 1, originY: 1, departedAtMs: 5 } }],
    ]);
    const next = updateTransitMemory(prev, [ship({ navStatus: 'DOCKED', waypoint: 'X1-VB74-B7', x: 10, y: -4 })], 1000);
    expect(next.get('TORWIND-6')).toEqual({ lastStationary: { waypoint: 'X1-VB74-B7', x: 10, y: -4 } });
  });

  it('creates transit memory on the stationary→transit flip, using the last stationary point as origin', () => {
    const prev = new Map<string, ShipMemory>([
      ['TORWIND-6', { lastStationary: { waypoint: 'X1-VB74-A1', x: 0, y: 0 } }],
    ]);
    const next = updateTransitMemory(
      prev,
      [ship({ navStatus: 'IN_TRANSIT', waypoint: 'X1-VB74-B7', x: 10, y: -4, arrivalTime: '2026-07-15T04:00:00Z' })],
      7000,
    );
    expect(next.get('TORWIND-6')).toEqual({
      lastStationary: { waypoint: 'X1-VB74-A1', x: 0, y: 0 },
      transit: { originWaypoint: 'X1-VB74-A1', originX: 0, originY: 0, departedAtMs: 7000 },
    });
  });

  it('keeps the original departure across subsequent transit polls', () => {
    const prev = new Map<string, ShipMemory>([
      ['TORWIND-6', {
        lastStationary: { waypoint: 'X1-VB74-A1', x: 0, y: 0 },
        transit: { originWaypoint: 'X1-VB74-A1', originX: 0, originY: 0, departedAtMs: 7000 },
      }],
    ]);
    const next = updateTransitMemory(
      prev,
      [ship({ navStatus: 'IN_TRANSIT', waypoint: 'X1-VB74-B7', x: 10, y: -4 })],
      12000,
    );
    expect(next.get('TORWIND-6')?.transit?.departedAtMs).toBe(7000);
  });

  it('creates no transit memory without a known prior stationary point (cold start mid-flight)', () => {
    const next = updateTransitMemory(new Map(), [ship({ navStatus: 'IN_TRANSIT', waypoint: 'X1-VB74-B7' })], 1000);
    expect(next.get('TORWIND-6')?.transit).toBeUndefined();
  });

  it('creates no transit memory when the destination equals the last stationary waypoint (origin unknowable)', () => {
    const prev = new Map<string, ShipMemory>([
      ['TORWIND-6', { lastStationary: { waypoint: 'X1-VB74-B7', x: 10, y: -4 } }],
    ]);
    const next = updateTransitMemory(prev, [ship({ navStatus: 'IN_TRANSIT', waypoint: 'X1-VB74-B7', x: 10, y: -4 })], 1000);
    expect(next.get('TORWIND-6')?.transit).toBeUndefined();
  });
});

describe('positionShip', () => {
  it('passes stationary ships through', () => {
    const p = positionShip(ship({ navStatus: 'DOCKED', x: 3, y: 4 }), undefined, 0);
    expect(p).toEqual({ x: 3, y: 4, mode: 'stationary', progress: null, headingRad: null });
  });

  it('interpolates along origin→destination by wall-clock fraction', () => {
    const mem: ShipMemory = {
      transit: { originWaypoint: 'A', originX: 0, originY: 0, departedAtMs: 0 },
    };
    const s = ship({ navStatus: 'IN_TRANSIT', waypoint: 'B', x: 100, y: 0, arrivalTime: new Date(10_000).toISOString() });
    const p = positionShip(s, mem, 5_000);
    expect(p.mode).toBe('exact');
    expect(p.x).toBeCloseTo(50);
    expect(p.y).toBeCloseTo(0);
    expect(p.progress).toBeCloseTo(0.5);
    expect(p.headingRad).toBeCloseTo(0); // flying +x
  });

  it('clamps past-arrival transits at the destination', () => {
    const mem: ShipMemory = {
      transit: { originWaypoint: 'A', originX: 0, originY: 0, departedAtMs: 0 },
    };
    const s = ship({ navStatus: 'IN_TRANSIT', waypoint: 'B', x: 100, y: 40, arrivalTime: new Date(10_000).toISOString() });
    const p = positionShip(s, mem, 60_000);
    expect(p.x).toBe(100);
    expect(p.y).toBe(40);
    expect(p.progress).toBe(1);
  });

  it('reports inbound (at destination, unknown progress) without transit memory', () => {
    const s = ship({ navStatus: 'IN_TRANSIT', waypoint: 'B', x: 100, y: 40, arrivalTime: new Date(10_000).toISOString() });
    const p = positionShip(s, undefined, 5_000);
    expect(p).toEqual({ x: 100, y: 40, mode: 'inbound', progress: null, headingRad: null });
  });

  it('reports inbound when transit memory exists but arrival is missing', () => {
    const mem: ShipMemory = {
      transit: { originWaypoint: 'A', originX: 0, originY: 0, departedAtMs: 0 },
    };
    const s = ship({ navStatus: 'IN_TRANSIT', waypoint: 'B', x: 100, y: 40, arrivalTime: null });
    expect(positionShip(s, mem, 5_000).mode).toBe('inbound');
  });
});
