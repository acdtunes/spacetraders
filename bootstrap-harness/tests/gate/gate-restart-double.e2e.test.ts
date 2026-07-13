import { describe, expect, it } from 'vitest';
import { twinGate } from '../helpers/twin-admin-gate';
import { gateEntry } from '../helpers/fixtures-gate';
import { resetDaemonDb, startTestDaemon } from '../helpers/daemon';
import { launchBootstrap, pollUntil, scrapeBootstrapMetric } from '../helpers/drive';
import { countCall } from '../helpers/mutation-log';

// ─── GWT ────────────────────────────────────────────────────────────────────────────
// GIVEN GATE construction has started and the first worker is sized,
// WHEN  the daemon is killed and rebooted TWICE back-to-back on the same DB + twin,
// THEN  nothing changes beyond a single uninterrupted run: phase stays sticky-GATE across BOTH
//       reboots, every exactly-once op stays exactly-once, no worker is re-bought, and construction
//       still completes.
//
// Bar coverage: (e) double/rapid restart is a no-op beyond (a)-(d) — this is the ONLY double-restart
//   spec, placed in GATE for the densest exactly-once guard set (construction-start, launch-autosizer,
//   executor-bounce) plus the sticky-phase invariant, so it stresses the most idempotence guards at once.
//   Uses the haulers:2 fixture (gate-worker-sizing) whose clean run BUYS the whole gate-delivery fleet
//   D = min(2 /v2-manifest chains + 1 delivery, 6) = 3, so PurchaseShip<=3 is a real /v2 no-double-buy
//   observable, not a flag.
//
// EXPECTED: GREEN. Each guard is a report-seam flag flipped exactly-once (a repeat report after any
//   reboot is a pure no-op) and phase stickiness re-derives from the persisted construction.started.
//   RED (the gap it would expose) if a second rapid reboot thrashes the phase (INCOME gauge lights /
//   GATE drops) or re-fires a guard — i.e. stickiness/idempotence held in daemon memory, not re-derived
//   from DB+twin on every boot. See st-drm-14 report, gaps #1 and #5.
describe('bootstrap GATE — double (back-to-back) restart idempotency', () => {
  it('two rapid reboots mid-construction change nothing: sticky GATE, guards once, no re-buy, completes', async () => {
    await twinGate.seedGate(gateEntry({ haulers: 2, gateMaterialChains: 4, credits: 3_000_000 }));
    await resetDaemonDb();

    // Sticky-GATE re-detection: after a boot the daemon must re-derive GATE from persisted
    // construction.started, never thrashing to INCOME. (Option B keeps the contract fleet earning through
    // GATE — nothing is repurposed — so income never collapses; the stickiness is belt-and-suspenders.)
    const expectStickyGate = async () => {
      const s = await pollUntil(() => twinGate.gateState(), (st) => st.construction.started, { steps: 30, advanceMs: 1000 });
      expect(s.construction.started).toBe(true);
      // EVENTUAL (poll): the gauge appears on the recovered brain's first reconcile tick, which the
      // twin-persisted construction.started check above does not wait for.
      const gateGauge = await pollUntil(
        () => scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' }),
        (v) => v === 1,
        { steps: 30, advanceMs: 1000 },
      );
      expect(gateGauge, 'rebooted daemon re-derives sticky GATE within its first reconcile ticks').toBe(1);
      expect(await scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(0);
    };

    let daemon = await startTestDaemon();
    try {
      launchBootstrap();
      // Lifetime 1: construction started + at least one worker sized.
      const mid = await pollUntil(
        () => twinGate.gateState(),
        (s) => s.construction.started && s.gateWorkers.length >= 1,
        { steps: 60, advanceMs: 1000 },
      );
      expect(countCall(mid.mutationLog, 'construction-start')).toBe(1);

      // ── Reboot #1 ──
      await daemon.stop();
      daemon = await startTestDaemon();
      launchBootstrap();
      await expectStickyGate();

      // ── Reboot #2 (immediately after #1) ──
      await daemon.stop();
      daemon = await startTestDaemon();
      launchBootstrap();
      await expectStickyGate();

      // Drive to COMPLETE and converge.
      await twinGate.setConstruction(100);
      const done = await pollUntil(() => twinGate.gateState(), (s) => s.done, { steps: 60, advanceMs: 1000 });

      // (b) every exactly-once op held across BOTH reboots.
      expect(countCall(done.mutationLog, 'construction-start')).toBe(1);         // never re-started
      expect(countCall(done.mutationLog, 'launch-autosizer')).toBeLessThanOrEqual(1);
      expect(countCall(done.mutationLog, 'executor-bounce')).toBeLessThanOrEqual(1);
      // (b) independent /v2 no-double-buy: the whole D=3 gate-delivery fleet is BOUGHT once (Option B —
      // nothing repurposed) and never re-bought — two reboots do not push this past D=3.
      expect(countCall(done.mutationLog, 'PurchaseShip')).toBeLessThanOrEqual(3);
      // (d) convergence.
      expect(done.done).toBe(true);
    } finally {
      await daemon.stop();
    }
  }, 300_000);
});
