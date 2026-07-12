import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { FastifyInstance } from 'fastify';
import type { World } from '../../src/world/types';
import { buildServer } from '../../src/server';
import { loadColdStartWorld, registerAgent } from '../../src/world/loader';
import { resetClock, setClockMode, setNow } from '../../src/clock';

// Hermetic Fastify-inject proof of POST /_twin/report — the daemon->twin seam that drives
// applyReport. The daemon (not the harness helper) POSTs one of the seven daemon-internal ops;
// the twin appends a mutation-log entry AND flips the paired /_twin/state flag exactly-once.
// Restart-idempotency: a repeated identical report must NOT double-append / double-flip.

const FROZEN_NOW = '2026-07-11T00:00:00.000Z';

// The 7 daemon-internal ops <-> their paired /_twin/state flag reader (post-op value in comment).
const OPS: Array<{ call: string; read: (s: Record<string, any>) => boolean }> = [
  { call: 'fleet-unassign', read: (s) => s.frigateContractTagged },              // true  -> false
  { call: 'batch-contract', read: (s) => s.batchContractRunning },               // false -> true
  { call: 'construction-start', read: (s) => s.construction.started },           // false -> true
  { call: 'executor-bounce', read: (s) => s.construction.adopted },              // false -> true
  { call: 'launch-autosizer', read: (s) => s.autosizerRunning },                 // false -> true
  { call: 'launch-siting', read: (s) => s.standingCoordinators.siting },         // false -> true
  { call: 'launch-worker-rebalancer', read: (s) => s.standingCoordinators.workerRebalancer }, // false -> true
];

function seededWorld(): World {
  const w = loadColdStartWorld();
  registerAgent(w, { symbol: 'TWINAGENT', faction: 'COSMIC', token: 'tok-1' });
  w.frigateContractTagged = true; // income-entry seed: gives fleet-unassign a true->false flip
  return w;
}

let app: FastifyInstance;
beforeEach(() => { resetClock(); setNow(FROZEN_NOW); setClockMode('frozen'); });
afterEach(async () => { if (app) await app.close(); });

async function report(call: string, detail?: Record<string, unknown>) {
  return app.inject({ method: 'POST', url: '/_twin/report', payload: detail ? { call, detail } : { call } });
}
async function state(): Promise<Record<string, any>> {
  const res = await app.inject({ method: 'GET', url: '/_twin/state' });
  expect(res.statusCode).toBe(200);
  return res.json();
}
function countCall(log: Array<{ call: string }>, call: string): number {
  return log.filter((e) => e.call === call).length; // mirrors the harness mutation-log helper
}

describe('POST /_twin/report — daemon->twin ingest (applyReport seam)', () => {
  it('flips each of the 7 paired flags exactly once; a second identical report is a no-op', async () => {
    app = buildServer({ world: seededWorld() });

    const pre = await state();
    expect(OPS.map((o) => o.read(pre))).toEqual([true, false, false, false, false, false, false]);

    for (const { call } of OPS) {
      const res = await report(call);
      expect(res.statusCode).toBeGreaterThanOrEqual(200);
      expect(res.statusCode).toBeLessThan(300);
    }

    const after = await state();
    expect(after.frigateContractTagged).toBe(false);
    expect(after.batchContractRunning).toBe(true);
    expect(after.construction.started).toBe(true);
    expect(after.construction.adopted).toBe(true);
    expect(after.autosizerRunning).toBe(true);
    expect(after.standingCoordinators).toEqual({ siting: true, workerRebalancer: true });
    for (const { call } of OPS) expect(countCall(after.mutationLog, call)).toBe(1);
    expect(after.mutationLog).toHaveLength(7);

    // Restart-idempotency: fire all 7 AGAIN -> no flips, no new entries (guard lives in applyReport).
    for (const { call } of OPS) expect((await report(call)).statusCode).toBeLessThan(300);
    const twice = await state();
    for (const { call } of OPS) expect(countCall(twice.mutationLog, call)).toBe(1);
    expect(twice.mutationLog).toHaveLength(7);
    expect(twice.frigateContractTagged).toBe(false);
  });

  it('stamps the world-clock `at` and passes report `detail` through to the entry', async () => {
    app = buildServer({ world: seededWorld() });
    expect((await report('batch-contract', { contractId: 'C-1' })).statusCode).toBeLessThan(300);
    const entry = (await state()).mutationLog.find((e: any) => e.call === 'batch-contract');
    expect(entry).toMatchObject({ call: 'batch-contract', detail: { contractId: 'C-1' }, at: FROZEN_NOW });
  });

  it('ignores an unrecognized call as a 2xx no-op (no flip, no log entry)', async () => {
    app = buildServer({ world: seededWorld() });
    expect((await report('not-a-real-op')).statusCode).toBeLessThan(300);
    expect((await state()).mutationLog).toHaveLength(0);
  });

  it('treats repurpose as a NON-report op — no PurchaseShip, no log entry, no worker added', async () => {
    app = buildServer({ world: seededWorld() });
    expect((await report('repurpose')).statusCode).toBeLessThan(300);
    const s = await state();
    expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(0);
    expect(countCall(s.mutationLog, 'repurpose')).toBe(0);
    expect(s.gateWorkers).toEqual([]);
    expect(s.mutationLog).toHaveLength(0);
  });

  it('rejects a missing/non-string call with the 400 error envelope', async () => {
    app = buildServer({ world: seededWorld() });
    const bad = await app.inject({ method: 'POST', url: '/_twin/report', payload: { detail: { x: 1 } } });
    expect(bad.statusCode).toBe(400);
    expect(bad.json().error.code).toBe(400);
  });
});
