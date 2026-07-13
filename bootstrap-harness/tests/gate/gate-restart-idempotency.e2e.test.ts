import { describe, expect, it } from 'vitest';
import { twinGate } from '../helpers/twin-admin-gate';
import { gateEntry } from '../helpers/fixtures-gate';
import { resetDaemonDb, startTestDaemon } from '../helpers/daemon';
import { launchBootstrap, pollUntil, scrapeBootstrapMetric } from '../helpers/drive';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap GATE — restart idempotency', () => {
  it('no double-start / no re-bounce / no double-worker-buy / no double-autosizer across a mid-GATE restart', async () => {
    await twinGate.seedGate(gateEntry({ haulers: 2, gateMaterialChains: 4, credits: 3_000_000 }));
    await resetDaemonDb();

    // Lifetime 1: run until construction started + at least one worker sized, then stop.
    let daemon = await startTestDaemon();
    let bouncesBefore = 0;
    try {
      launchBootstrap();
      const mid = await pollUntil(
        () => twinGate.gateState(),
        (s) => s.construction.started && s.gateWorkers.length >= 1,
        { steps: 60, advanceMs: 1000 },
      );
      bouncesBefore = countCall(mid.mutationLog, 'executor-bounce');
      expect(countCall(mid.mutationLog, 'construction-start')).toBe(1);
    } finally {
      // In a finally: a failed arrange must never LEAK a live daemon (leaked lifetime-1 daemons keep
      // reconciling against the shared twin and poison every later attempt/spec).
      await daemon.stop();
    }
    daemon = await startTestDaemon(); // reboot; same DB + twin (construction + workers persist)
    try {
      launchBootstrap();
      // (a) STICKY phase re-detection ACROSS the restart — the crux of "GATE sticky once construction
      // started". construction.started persisted (twin) → the rebooted daemon must re-derive GATE from
      // it BEFORE we force completion, never thrashing back to INCOME. (Under Option B the contract fleet
      // keeps earning through GATE — nothing is repurposed — so income never even collapses; stickiness is
      // now belt-and-suspenders.) gate-sticky proves this WITHOUT a restart; this is the first assertion
      // of it SURVIVING a reboot.
      const reGate = await pollUntil(() => twinGate.gateState(), (s) => s.construction.started, { steps: 30, advanceMs: 1000 });
      expect(reGate.construction.started).toBe(true);
      // EVENTUAL (poll): construction.started is twin-persisted and true instantly after the reboot;
      // the gauge only appears on the recovered brain's first reconcile tick.
      const gateGauge = await pollUntil(
        () => scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' }),
        (v) => v === 1,
        { steps: 30, advanceMs: 1000 },
      );
      expect(gateGauge, 'rebooted daemon re-derives sticky GATE within its first reconcile ticks').toBe(1);
      expect(await scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(0);
      // Drive to COMPLETE.
      await twinGate.setConstruction(100);
      const done = await pollUntil(() => twinGate.gateState(), (s) => s.done, { steps: 60, advanceMs: 1000 });
      // Guards held across the restart:
      expect(countCall(done.mutationLog, 'construction-start')).toBe(1);         // not re-started
      expect(countCall(done.mutationLog, 'executor-bounce')).toBeLessThanOrEqual(Math.max(1, bouncesBefore)); // not re-bounced once adopted
      expect(countCall(done.mutationLog, 'launch-autosizer')).toBe(1);           // launched once total
      // "no double-worker-buy" — the title's core claim, previously UNASSERTED. Independent /v2
      // observable (not the report-seam gateWorkers flag): under Option B a clean run BUYS the whole
      // gate-delivery fleet D = min(2 manifest chains + 1 delivery, 6) = 3 (see gate-worker-sizing, same
      // /v2 manifest); re-buying a mid-flight worker after the restart would push this past D=3.
      expect(countCall(done.mutationLog, 'PurchaseShip')).toBeLessThanOrEqual(3);
      expect(done.done).toBe(true);
    } finally {
      await daemon.stop();
    }
  }, 300_000);
});
