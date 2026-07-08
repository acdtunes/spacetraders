import { describe, it, expect } from 'vitest';
import {
  DEMO_EVENT_CYCLE,
  DEMO_EVENT_INTERVAL_MS,
  DEMO_EVENT_MAX_LIMIT,
  DEMO_GATE_BASE_PROGRESS,
  DEMO_GATE_DELIVERY_TYPE,
  DEMO_GATE_EPOCH_MS,
  DEMO_GATE_TICK_PER_DELIVERY,
  DEMO_SHIP_SCOPED_TYPES,
  DEMO_SIGNAL_LOSS,
  DEMO_SIGNAL_LOSS_DURATION_MS,
  DEMO_SIGNAL_LOSS_PERIOD_MS,
  demoEventTicker,
  demoGateProgress,
  demoLatestEventId,
  isSignalLossWindow,
} from '../demoEvents';
import { mockState } from '../mockScenario';

// A convenient interval-aligned clock (divisible by DEMO_EVENT_INTERVAL_MS).
const T0 = 800_000_000;

// The REAL gobot captain_events type strings — the single source of truth is
// gobot/internal/domain/captain/events.go. The demo cycle must never drift to
// invented strings or domain/eventFx would demote every effect to a generic ping.
const REAL_GOBOT_EVENT_TYPES = [
  'workflow.finished',
  'workflow.failed',
  'container.crashed',
  'container.crashloop',
  'container.heartbeat_lost',
  'ship.idle',
  'credits.threshold',
  'contract.completed',
  'contract.failed',
  'income.stalled',
  'stream.down',
  'deploy.completed',
];

describe('demoEventTicker', () => {
  it('emits exactly one new event per ~8s bucket with a monotonically increasing id', () => {
    const headAt = (t: number) => demoEventTicker(t)[0];

    const head = headAt(T0);
    // Same 8s bucket -> same newest event.
    expect(headAt(T0 + DEMO_EVENT_INTERVAL_MS - 1).id).toBe(head.id);
    // Next bucket -> exactly one new id.
    expect(headAt(T0 + DEMO_EVENT_INTERVAL_MS).id).toBe(head.id + 1);
    expect(headAt(T0 + 3 * DEMO_EVENT_INTERVAL_MS).id).toBe(head.id + 3);
  });

  it('returns newest-first with contiguous descending ids', () => {
    const events = demoEventTicker(T0, { limit: 10 });
    expect(events).toHaveLength(10);
    expect(events[0].id).toBe(demoLatestEventId(T0));
    for (let i = 1; i < events.length; i++) {
      expect(events[i].id).toBe(events[i - 1].id - 1);
    }
  });

  it('cycles only REAL gobot captain_events type strings', () => {
    // The configured cycle itself is drawn from the real vocabulary...
    for (const type of DEMO_EVENT_CYCLE) {
      expect(REAL_GOBOT_EVENT_TYPES).toContain(type);
    }
    // ...and one full cycle of emitted events uses every configured type.
    const events = demoEventTicker(T0, { limit: DEMO_EVENT_CYCLE.length });
    for (const event of events) {
      expect(REAL_GOBOT_EVENT_TYPES).toContain(event.type);
    }
    expect(new Set(events.map((e) => e.type))).toEqual(new Set(DEMO_EVENT_CYCLE));
    // The headline strings from the slice spec are all in rotation.
    for (const required of ['workflow.finished', 'contract.completed', 'credits.threshold', 'ship.idle']) {
      expect(DEMO_EVENT_CYCLE).toContain(required);
    }
  });

  it('draws ship symbols from the mock fleet for ship-scoped types, null otherwise', () => {
    const fleetSymbols = new Set(mockState.ships.map((ship) => ship.symbol));
    expect(fleetSymbols.size).toBeGreaterThan(0);

    const events = demoEventTicker(T0, { limit: 3 * DEMO_EVENT_CYCLE.length });
    for (const event of events) {
      if (DEMO_SHIP_SCOPED_TYPES.has(event.type)) {
        expect(event.ship).not.toBeNull();
        expect(fleetSymbols.has(event.ship as string)).toBe(true);
      } else {
        expect(event.ship).toBeNull();
      }
    }
    // Round-robin: ship-scoped events do not all pin to a single hull.
    const shipsUsed = new Set(events.map((e) => e.ship).filter((s) => s !== null));
    expect(shipsUsed.size).toBeGreaterThan(1);
  });

  it('honors the after-id cursor (delta fetches return only newer events)', () => {
    const latest = demoLatestEventId(T0);
    const delta = demoEventTicker(T0, { afterId: latest - 4 });
    expect(delta.map((e) => e.id)).toEqual([latest, latest - 1, latest - 2, latest - 3]);
    // Fully caught up -> empty page, not an error.
    expect(demoEventTicker(T0, { afterId: latest })).toEqual([]);
  });

  it('caps the page size like the live endpoint and is empty before any event exists', () => {
    expect(demoEventTicker(T0, { limit: 10_000 })).toHaveLength(DEMO_EVENT_MAX_LIMIT);
    expect(demoEventTicker(T0)).toHaveLength(50); // server default
    expect(demoEventTicker(0)).toEqual([]);
    expect(demoEventTicker(DEMO_EVENT_INTERVAL_MS - 1)).toEqual([]);
  });

  it('stamps createdAt from the id clock (newer id -> later timestamp)', () => {
    const [newer, older] = demoEventTicker(T0, { limit: 2 });
    expect(Date.parse(newer.createdAt) - Date.parse(older.createdAt)).toBe(DEMO_EVENT_INTERVAL_MS);
  });
});

describe('demoGateProgress', () => {
  /** Delivery-type events emitted in (DEMO_GATE_EPOCH_MS, t] per the ticker itself. */
  const deliveriesInWindow = (t: number): number =>
    demoEventTicker(t, {
      afterId: demoLatestEventId(DEMO_GATE_EPOCH_MS),
      limit: DEMO_EVENT_MAX_LIMIT,
    }).filter((e) => e.type === DEMO_GATE_DELIVERY_TYPE).length;

  it('opens at the base progress at the demo epoch', () => {
    expect(demoGateProgress(DEMO_GATE_EPOCH_MS).progress).toBeCloseTo(DEMO_GATE_BASE_PROGRESS, 5);
  });

  it('ticks up by +0.1 per delivery-type event', () => {
    // 60 buckets (= 5 full cycles) past the epoch: expectation derived from the
    // ticker's own delivery events so the two synthesizers can never disagree.
    const t = DEMO_GATE_EPOCH_MS + 60 * DEMO_EVENT_INTERVAL_MS;
    const deliveries = deliveriesInWindow(t);
    expect(deliveries).toBeGreaterThan(0);
    expect(demoGateProgress(t).progress).toBeCloseTo(
      DEMO_GATE_BASE_PROGRESS + DEMO_GATE_TICK_PER_DELIVERY * deliveries,
      5
    );
  });

  it('is monotonically non-decreasing over time', () => {
    let previous = -Infinity;
    for (let step = 0; step <= 200; step++) {
      // Uneven stride so samples land on both bucket edges and mid-bucket.
      const t = DEMO_GATE_EPOCH_MS + step * (DEMO_EVENT_INTERVAL_MS + 1_777);
      const { progress } = demoGateProgress(t);
      expect(progress).not.toBeNull();
      expect(progress as number).toBeGreaterThanOrEqual(previous);
      previous = progress as number;
    }
  });

  it('clamps at 100 and fulfills the whole bill once saturated', () => {
    const yearLater = DEMO_GATE_EPOCH_MS + 365 * 24 * 60 * 60 * 1000;
    const gate = demoGateProgress(yearLater);
    expect(gate.progress).toBe(100);
    for (const material of gate.materials) {
      expect(material.fulfilled).toBe(material.required);
    }
  });

  it('keeps materials unit-consistent with the aggregate progress', () => {
    const t = DEMO_GATE_EPOCH_MS + 100 * DEMO_EVENT_INTERVAL_MS;
    const gate = demoGateProgress(t);
    const totalRequired = gate.materials.reduce((sum, m) => sum + m.required, 0);
    const totalFulfilled = gate.materials.reduce((sum, m) => sum + m.fulfilled, 0);
    expect(totalFulfilled).toBe(Math.round((totalRequired * (gate.progress as number)) / 100));
    for (const material of gate.materials) {
      expect(material.fulfilled).toBeGreaterThanOrEqual(0);
      expect(material.fulfilled).toBeLessThanOrEqual(material.required);
    }
  });
});

describe('isSignalLossWindow', () => {
  const PERIOD = DEMO_SIGNAL_LOSS_PERIOD_MS;
  const DURATION = DEMO_SIGNAL_LOSS_DURATION_MS;
  const WINDOW_START = PERIOD - DURATION;

  it('uses the ~3min/~20s drill geometry from the slice spec', () => {
    expect(PERIOD).toBe(180_000);
    expect(DURATION).toBe(20_000);
    expect(DEMO_SIGNAL_LOSS).toBe(true); // drill is on by default in demo mode
  });

  it('is dark exactly for the trailing 20s of every 3-minute cycle', () => {
    // First cycle boundaries.
    expect(isSignalLossWindow(0)).toBe(false);
    expect(isSignalLossWindow(WINDOW_START - 1)).toBe(false);
    expect(isSignalLossWindow(WINDOW_START)).toBe(true);
    expect(isSignalLossWindow(PERIOD - 1)).toBe(true);
    // The instant the window passes, the signal auto-recovers.
    expect(isSignalLossWindow(PERIOD)).toBe(false);

    // And the drill recurs every cycle, indefinitely.
    const manyCyclesLater = 41 * PERIOD;
    expect(isSignalLossWindow(manyCyclesLater + WINDOW_START - 1)).toBe(false);
    expect(isSignalLossWindow(manyCyclesLater + WINDOW_START)).toBe(true);
    expect(isSignalLossWindow(manyCyclesLater + PERIOD - 1)).toBe(true);
    expect(isSignalLossWindow(manyCyclesLater + PERIOD)).toBe(false);
  });

  it('never fires when the DEMO_SIGNAL_LOSS gate is off', () => {
    expect(isSignalLossWindow(WINDOW_START, false)).toBe(false);
    expect(isSignalLossWindow(PERIOD - 1, false)).toBe(false);
  });
});
