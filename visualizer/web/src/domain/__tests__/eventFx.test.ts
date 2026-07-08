import { describe, it, expect } from 'vitest';
import {
  FX_TTL_MS,
  findGateWaypoint,
  fxAnchor,
  fxForEvent,
  fxKindForEventType,
  fxProgress,
  pruneFx,
  type FxInstance,
  type FxKind,
  type FxPositionResolver,
} from '../eventFx';
import { GATE_WAYPOINT } from '../../constants/api';
import type { FleetEvent, Waypoint, WaypointType } from '../../types/spacetraders';

const NOW = 1_000_000;

const event = (overrides: Partial<FleetEvent> = {}): FleetEvent => ({
  id: 42,
  type: 'workflow.finished',
  ship: 'ADALA-3',
  createdAt: '2026-07-08T00:00:00Z',
  processed: false,
  ...overrides,
});

/** Resolver that always anchors at a fixed point. */
const at = (x: number, y: number) => () => ({ x, y });
/** Resolver for events with no scene anchor (feed-only path). */
const nowhere = () => null;

const fx = (overrides: Partial<FxInstance> = {}): FxInstance => ({
  key: '1',
  kind: 'arrival-ripple',
  x: 0,
  y: 0,
  bornAt: NOW,
  ttlMs: FX_TTL_MS['arrival-ripple'],
  ...overrides,
});

// The REAL gobot captain_events type strings that carry no dedicated scene FX.
const FEED_ONLY_TYPES = [
  'workflow.failed',
  'container.crashed',
  'container.crashloop',
  'container.heartbeat_lost',
  'ship.idle',
  'income.stalled',
  'stream.down',
  'contract.failed',
  'deploy.completed',
];

describe('eventFx', () => {
  describe('fxKindForEventType (real gobot captain_events strings)', () => {
    it('maps workflow.finished -> arrival-ripple', () => {
      expect(fxKindForEventType('workflow.finished')).toBe('arrival-ripple');
    });

    it('maps contract.completed -> gate-flash', () => {
      expect(fxKindForEventType('contract.completed')).toBe('gate-flash');
    });

    it('maps credits.threshold -> income-ping', () => {
      expect(fxKindForEventType('credits.threshold')).toBe('income-ping');
    });

    it('maps every other real event type -> generic-ping', () => {
      for (const type of FEED_ONLY_TYPES) {
        expect(fxKindForEventType(type)).toBe('generic-ping');
      }
    });

    it('maps a never-seen-before type -> generic-ping (forward compatible)', () => {
      expect(fxKindForEventType('some.future.event')).toBe('generic-ping');
    });
  });

  describe('fxAnchor', () => {
    it('anchors ripples and income pings to the ship, gate-flash to the gate', () => {
      expect(fxAnchor('arrival-ripple')).toBe('ship');
      expect(fxAnchor('income-ping')).toBe('ship');
      expect(fxAnchor('gate-flash')).toBe('gate');
    });

    it('marks generic-ping as feed-only (no scene anchor)', () => {
      expect(fxAnchor('generic-ping')).toBe('none');
    });
  });

  describe('fxForEvent', () => {
    it('spawns an arrival-ripple at the resolved ship position for workflow.finished', () => {
      const result = fxForEvent(event({ id: 7, type: 'workflow.finished' }), at(12, -34), NOW);
      expect(result).toEqual({
        key: '7',
        kind: 'arrival-ripple',
        x: 12,
        y: -34,
        bornAt: NOW,
        ttlMs: 2400,
      });
    });

    it('spawns a gate-flash at the resolved gate position for contract.completed', () => {
      const result = fxForEvent(
        event({ id: 8, type: 'contract.completed', ship: null }),
        at(100, 200),
        NOW
      );
      expect(result?.kind).toBe('gate-flash');
      expect(result?.x).toBe(100);
      expect(result?.y).toBe(200);
      expect(result?.ttlMs).toBe(3000);
    });

    it('passes the mapped kind to the resolver so it can pick the anchor', () => {
      const seen: FxKind[] = [];
      fxForEvent(event({ type: 'contract.completed' }), (_e, kind) => {
        seen.push(kind);
        return { x: 0, y: 0 };
      }, NOW);
      expect(seen).toEqual(['gate-flash']);
    });

    it('spawns an income-ping for credits.threshold', () => {
      const result = fxForEvent(event({ id: 9, type: 'credits.threshold' }), at(1, 2), NOW);
      expect(result?.kind).toBe('income-ping');
      expect(result?.ttlMs).toBe(1500);
    });

    it('keys every instance by String(event.id)', () => {
      const result = fxForEvent(event({ id: 12345 }), at(0, 0), NOW);
      expect(result?.key).toBe('12345');
      expect(result?.key).toBe(String(12345));
    });

    it('returns null (feed-only) for every other real type when the anchor resolves null', () => {
      for (const type of FEED_ONLY_TYPES) {
        expect(fxForEvent(event({ type }), nowhere, NOW)).toBeNull();
      }
    });

    it('tolerates a null position even for scene-mapped kinds (ship off-map) -> null', () => {
      // e.g. workflow.finished for a ship filtered out of the current system
      expect(fxForEvent(event({ type: 'workflow.finished' }), nowhere, NOW)).toBeNull();
      expect(fxForEvent(event({ type: 'contract.completed' }), nowhere, NOW)).toBeNull();
      expect(fxForEvent(event({ type: 'credits.threshold' }), nowhere, NOW)).toBeNull();
    });
  });

  describe('TTLs', () => {
    it('uses the specified lifetimes per kind', () => {
      expect(FX_TTL_MS['arrival-ripple']).toBe(2400);
      expect(FX_TTL_MS['gate-flash']).toBe(3000);
      expect(FX_TTL_MS['income-ping']).toBe(1500);
    });
  });

  describe('fxProgress', () => {
    it('goes 0 at birth -> 1 at expiry, clamped on both sides', () => {
      const instance = fx({ bornAt: NOW, ttlMs: 2400 });
      expect(fxProgress(instance, NOW)).toBe(0);
      expect(fxProgress(instance, NOW + 1200)).toBeCloseTo(0.5);
      expect(fxProgress(instance, NOW + 2400)).toBe(1);
      expect(fxProgress(instance, NOW + 99999)).toBe(1);
      expect(fxProgress(instance, NOW - 50)).toBe(0);
    });
  });

  describe('pruneFx', () => {
    it('drops instances that reached their ttl and keeps live ones', () => {
      const young = fx({ key: 'young', bornAt: NOW, ttlMs: 2400 });
      const mid = fx({ key: 'mid', kind: 'gate-flash', bornAt: NOW - 2999, ttlMs: 3000 });
      const exactlyExpired = fx({ key: 'edge', bornAt: NOW - 2400, ttlMs: 2400 });
      const longDead = fx({ key: 'dead', kind: 'income-ping', bornAt: NOW - 10_000, ttlMs: 1500 });

      const pruned = pruneFx([young, mid, exactlyExpired, longDead], NOW);
      expect(pruned.map((f) => f.key)).toEqual(['young', 'mid']);
    });

    it('returns the same array identity when nothing expired (per-frame churn guard)', () => {
      const list = [fx({ bornAt: NOW })];
      expect(pruneFx(list, NOW + 1)).toBe(list);
      expect(pruneFx([], NOW)).toEqual([]);
    });

    it('empties fully once every ttl has elapsed', () => {
      const list = [
        fx({ key: 'a', bornAt: NOW, ttlMs: 2400 }),
        fx({ key: 'b', kind: 'gate-flash', bornAt: NOW, ttlMs: 3000 }),
        fx({ key: 'c', kind: 'income-ping', bornAt: NOW, ttlMs: 1500 }),
      ];
      expect(pruneFx(list, NOW + 3000)).toEqual([]);
    });
  });

  // ── Regression: gate-flash must render in demo (the S1 "invisible feature") ──
  // The live-era gate symbol GATE_WAYPOINT ('X1-PZ28-…') never appears in demo
  // data, whose waypoints are X1-DEMO-*/X1-GRID-*/X1-TWIN-*. Before the fallback,
  // the gate lookup returned undefined there, so contract.completed (a frequent
  // demo event) silently produced no FX while the other domain tests — which
  // stub the resolver with a fixed point — stayed green. These exercise the REAL
  // waypoint lookup against demo-shaped data.
  describe('findGateWaypoint (real gate anchor lookup)', () => {
    const wp = (symbol: string, type: WaypointType, x: number, y: number): Waypoint => ({
      symbol,
      type,
      systemSymbol: 'X1-DEMO',
      x,
      y,
      orbitals: [],
      traits: [],
      isUnderConstruction: false,
    });

    // Mirrors the demo scenario shape: a JUMP_GATE (X1-DEMO-JG1 @ 45,220) among
    // non-gate waypoints, and crucially NO live-era X1-PZ28 gate symbol.
    const demoWaypoints = new Map<string, Waypoint>([
      ['X1-DEMO-A1', wp('X1-DEMO-A1', 'PLANET', 10, 10)],
      ['X1-DEMO-M2', wp('X1-DEMO-M2', 'MOON', 5, 5)],
      ['X1-DEMO-JG1', wp('X1-DEMO-JG1', 'JUMP_GATE', 45, 220)],
    ]);

    it('prefers the exact GATE_WAYPOINT symbol when it is present (live path)', () => {
      const liveWaypoints = new Map(demoWaypoints);
      liveWaypoints.set(GATE_WAYPOINT, wp(GATE_WAYPOINT, 'JUMP_GATE', 999, 999));
      expect(findGateWaypoint(liveWaypoints, GATE_WAYPOINT)?.symbol).toBe(GATE_WAYPOINT);
    });

    it('falls back to the first JUMP_GATE when GATE_WAYPOINT is absent (demo path)', () => {
      expect(demoWaypoints.has(GATE_WAYPOINT)).toBe(false);
      expect(findGateWaypoint(demoWaypoints, GATE_WAYPOINT)?.symbol).toBe('X1-DEMO-JG1');
    });

    it('returns undefined only when the map holds no jump gate at all', () => {
      const gateless = new Map<string, Waypoint>([
        ['X1-DEMO-A1', wp('X1-DEMO-A1', 'PLANET', 10, 10)],
      ]);
      expect(findGateWaypoint(gateless, GATE_WAYPOINT)).toBeUndefined();
    });

    it('drives fxForEvent to a NON-NULL gate-flash for contract.completed in demo', () => {
      // The REAL resolver logic (not a fixed-point stub): for a gate anchor, look
      // the gate up in the waypoints map exactly as EventFxLayer does, then
      // anchor at its coordinates.
      const demoResolver: FxPositionResolver = (_event, kind) => {
        if (fxAnchor(kind) !== 'gate') return null;
        const gate = findGateWaypoint(demoWaypoints, GATE_WAYPOINT);
        return gate ? { x: gate.x, y: gate.y } : null;
      };

      const result = fxForEvent(
        event({ id: 501, type: 'contract.completed', ship: null }),
        demoResolver,
        NOW
      );

      expect(result).not.toBeNull();
      expect(result?.kind).toBe('gate-flash');
      expect(result?.x).toBe(45);
      expect(result?.y).toBe(220);
      expect(result?.ttlMs).toBe(FX_TTL_MS['gate-flash']);
    });
  });
});
