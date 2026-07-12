import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { World } from '../../src/world/types';
import { loadColdStartWorld, newControlPlaneState } from '../../src/world/loader';
import { appendMutation, applyReport } from '../../src/world/mutation-log';
import { advanceClock, resetClock, setClockMode, setNow } from '../../src/clock';

// Pin the WORLD clock to a known FROZEN instant so `at` is deterministic. Real timers so the
// frozen clock never drifts on wall time.
beforeEach(() => {
  vi.useRealTimers();
  resetClock();
  setNow('2026-07-11T00:00:00.000Z');
  setClockMode('frozen');
});
afterEach(() => { vi.useRealTimers(); });

describe('newControlPlaneState / cold defaults (the interface endpoints consume)', () => {
  it('loadColdStartWorld initializes EVERY control-plane field to its cold default', () => {
    const w = loadColdStartWorld();
    expect(w.mutationLog).toEqual([]);
    expect(w.coverage).toBe(0);
    expect(w.marketScouting.size).toBe(0);
    expect(w.haulers).toEqual([]);
    expect(w.frigateContractTagged).toBe(false);
    expect(w.batchContractRunning).toBe(false);
    expect(w.creditsPerHour).toBe(0);
    expect(w.hubs).toEqual([]);
    expect(w.construction).toEqual({ site: '', percent: 0, started: false, adopted: false });
    expect(w.gateWorkers).toEqual([]);
    expect(w.executorRunning).toBe(false);
    expect(w.autosizerRunning).toBe(false);
    expect(w.standingCoordinators).toEqual({ siting: false, workerRebalancer: false });
    expect(w.done).toBe(false);
  });

  it('returns FRESH (non-shared) collections on each call — two worlds never alias', () => {
    const a = newControlPlaneState();
    const b = newControlPlaneState();
    a.mutationLog.push({ seq: 1, call: 'x', at: '2026-07-11T00:00:00.000Z' });
    a.marketScouting.set('X1-PZ28-A1', { scouted: true, fresh: true });
    a.hubs.push('X1-PZ28-H1');
    a.construction.started = true;
    a.standingCoordinators.siting = true;
    expect(b.mutationLog).toEqual([]);
    expect(b.marketScouting.size).toBe(0);
    expect(b.hubs).toEqual([]);
    expect(b.construction.started).toBe(false);
    expect(b.standingCoordinators.siting).toBe(false);
  });
});

describe('appendMutation (seq + at)', () => {
  it('stamps a monotonic 1-indexed seq and the WORLD-clock now as `at`', () => {
    const w = loadColdStartWorld();
    const e1 = appendMutation(w, 'PurchaseShip', { shipType: 'SHIP_PROBE' });
    expect(e1).toEqual({ seq: 1, call: 'PurchaseShip', detail: { shipType: 'SHIP_PROBE' }, at: '2026-07-11T00:00:00.000Z' });
    expect(w.mutationLog).toHaveLength(1);
    expect(w.mutationLog[0]).toBe(e1); // the exact entry is pushed, not a copy
  });

  it('advances seq and re-stamps `at` from the (advanced) world clock — distinct at per advance', () => {
    const w = loadColdStartWorld();
    const e1 = appendMutation(w, 'PurchaseShip');
    advanceClock(1000); // harness clock-step
    const e2 = appendMutation(w, 'PurchaseShip');
    expect(e1.seq).toBe(1);
    expect(e2.seq).toBe(2);
    expect(e1.at).toBe('2026-07-11T00:00:00.000Z');
    expect(e2.at).toBe('2026-07-11T00:00:01.000Z');
    expect(e1.at).not.toBe(e2.at);
  });

  it('omits `detail` entirely when none is provided', () => {
    const w = loadColdStartWorld();
    const e = appendMutation(w, 'navigate');
    expect('detail' in e).toBe(false);
    expect(e).toEqual({ seq: 1, call: 'navigate', at: '2026-07-11T00:00:00.000Z' });
  });

  it('keeps seq monotonic + gap-free across many appends at the same frozen instant', () => {
    const w = loadColdStartWorld();
    for (let i = 1; i <= 5; i++) expect(appendMutation(w, 'PurchaseShip').seq).toBe(i);
    expect(w.mutationLog.map((m) => m.seq)).toEqual([1, 2, 3, 4, 5]);
  });
});

describe('applyReport (each paired flag flips exactly once; repeat call is a no-op)', () => {
  it('fleet-unassign flips frigateContractTagged true->false, then no-ops', () => {
    const w = loadColdStartWorld();
    w.frigateContractTagged = true; // income-entry seed
    const first = applyReport(w, { call: 'fleet-unassign' });
    expect(first).not.toBeNull();
    expect(w.frigateContractTagged).toBe(false);
    expect(w.mutationLog).toHaveLength(1);
    expect(w.mutationLog[0].call).toBe('fleet-unassign');

    const second = applyReport(w, { call: 'fleet-unassign' });
    expect(second).toBeNull();
    expect(w.frigateContractTagged).toBe(false);
    expect(w.mutationLog).toHaveLength(1); // no duplicate entry
  });

  // The six false->true report flips, each guarded by its OWN target flag.
  const trueFlips: Array<{ call: string; read: (w: World) => boolean }> = [
    { call: 'batch-contract', read: (w) => w.batchContractRunning },
    { call: 'construction-start', read: (w) => w.construction.started },
    { call: 'executor-bounce', read: (w) => w.construction.adopted },
    { call: 'launch-autosizer', read: (w) => w.autosizerRunning },
    { call: 'launch-siting', read: (w) => w.standingCoordinators.siting },
    { call: 'launch-worker-rebalancer', read: (w) => w.standingCoordinators.workerRebalancer },
  ];
  for (const { call, read } of trueFlips) {
    it(`${call} flips its flag false->true exactly once`, () => {
      const w = loadColdStartWorld();
      expect(read(w)).toBe(false);

      const first = applyReport(w, { call });
      expect(first).not.toBeNull();
      expect(first!.call).toBe(call);
      expect(read(w)).toBe(true);
      expect(w.mutationLog).toHaveLength(1);

      const second = applyReport(w, { call });
      expect(second).toBeNull();
      expect(read(w)).toBe(true);
      expect(w.mutationLog).toHaveLength(1); // idempotent — still one entry
    });
  }

  it('executor-bounce guards on construction.adopted, NOT executorRunning (bounces even when seeded running)', () => {
    const w = loadColdStartWorld();
    w.executorRunning = true; // gate-entry seeds executorRunning:true
    expect(w.construction.adopted).toBe(false);
    const first = applyReport(w, { call: 'executor-bounce' });
    expect(first).not.toBeNull();
    expect(w.construction.adopted).toBe(true);
    expect(applyReport(w, { call: 'executor-bounce' })).toBeNull(); // still exactly-once
    expect(w.mutationLog).toHaveLength(1);
  });

  it('passes report `detail` and the world-clock `at` through to the appended entry', () => {
    const w = loadColdStartWorld();
    advanceClock(5000);
    const e = applyReport(w, { call: 'batch-contract', detail: { contractId: 'C-1' } });
    expect(e).toEqual({ seq: 1, call: 'batch-contract', detail: { contractId: 'C-1' }, at: '2026-07-11T00:00:05.000Z' });
    expect(w.mutationLog[0]).toBe(e);
  });

  it('report entries share the ONE monotonic seq stream with direct appends', () => {
    const w = loadColdStartWorld();
    appendMutation(w, 'PurchaseShip');                    // seq 1
    const e = applyReport(w, { call: 'launch-siting' });  // seq 2
    appendMutation(w, 'navigate');                        // seq 3
    expect(e!.seq).toBe(2);
    expect(w.mutationLog.map((m) => m.seq)).toEqual([1, 2, 3]);
  });

  it('ignores an unrecognized report call — no flip, no log entry, returns null', () => {
    const w = loadColdStartWorld();
    const out = applyReport(w, { call: 'not-a-real-op' });
    expect(out).toBeNull();
    expect(w.mutationLog).toHaveLength(0);
  });

  it('flips each distinct paired flag independently in one run (all seven ops)', () => {
    const w = loadColdStartWorld();
    w.frigateContractTagged = true;
    for (const call of ['fleet-unassign', 'batch-contract', 'construction-start', 'executor-bounce',
      'launch-autosizer', 'launch-siting', 'launch-worker-rebalancer']) {
      expect(applyReport(w, { call })).not.toBeNull();
    }
    expect(w.mutationLog).toHaveLength(7);
    expect(w.mutationLog.map((m) => m.seq)).toEqual([1, 2, 3, 4, 5, 6, 7]);
    expect(w.frigateContractTagged).toBe(false);
    expect(w.batchContractRunning).toBe(true);
    expect(w.construction.started).toBe(true);
    expect(w.construction.adopted).toBe(true);
    expect(w.autosizerRunning).toBe(true);
    expect(w.standingCoordinators).toEqual({ siting: true, workerRebalancer: true });
  });
});
