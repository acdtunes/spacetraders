import { describe, expect, it } from 'vitest';
import { twinIncome } from '../helpers/twin-admin-income';
import { incomeEntry } from '../helpers/fixtures-income';
import { resetDaemonDb, startTestDaemon } from '../helpers/daemon';
import { seedDaemonMarketCoverage } from '../helpers/daemon-seed';
import { launchBootstrap, pollUntil, scrapeBootstrapMetric } from '../helpers/drive';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap INCOME — restart idempotency', () => {
  it('no double-buy / no re-retire / no double batch-contract across a mid-purchase restart', async () => {
    await twinIncome.seedIncome(incomeEntry({ credits: 3_000_000 }));
    await resetDaemonDb();
    await seedDaemonMarketCoverage(); // DATA-complete coverage in the daemon's local DB (persists across
    // the reboot below — resetDaemonDb is NOT re-run) so both lifetimes derive INCOME, not DATA.

    // Lifetime 1: run until the FIRST hauler purchase is recorded, then stop before the next observe.
    let daemon = await startTestDaemon();
    let retiresBefore = 0;
    let batchBefore = 0;
    try {
      launchBootstrap();
      const afterFirst = await pollUntil(
        () => twinIncome.incomeState(),
        (s) => countCall(s.mutationLog, 'PurchaseShip') >= 1,
        { steps: 60, advanceMs: 1000 },
      );
      expect(countCall(afterFirst.mutationLog, 'PurchaseShip')).toBe(1);
      retiresBefore = countCall(afterFirst.mutationLog, 'fleet-unassign');
      batchBefore = countCall(afterFirst.mutationLog, 'batch-contract');
    } finally {
      // In a finally: a failed arrange must never LEAK a live daemon (leaked lifetime-1 daemons keep
      // reconciling against the shared twin and poison every later attempt/spec).
      await daemon.stop();
    }
    daemon = await startTestDaemon(); // reboot; same test DB + twin world (1 hauler persists)
    try {
      launchBootstrap();
      const done = await pollUntil(
        () => twinIncome.incomeState(),
        (s) => s.haulers.filter((h) => h.parkedHub).length >= 4,
        { steps: 80, advanceMs: 1000 },
      );
      // Exactly hauler_target (4) hauler buys across BOTH lifetimes — the mid-flight hauler is not re-bought.
      expect(countCall(done.mutationLog, 'PurchaseShip')).toBe(4);
      // Frigate retired at most once total; batch-contract launched at most once total.
      expect(countCall(done.mutationLog, 'fleet-unassign')).toBeLessThanOrEqual(Math.max(1, retiresBefore));
      expect(countCall(done.mutationLog, 'batch-contract')).toBeLessThanOrEqual(Math.max(1, batchBefore, 1));
      expect(done.frigateContractTagged).toBe(false);
      // (a) phase re-detection after the reboot lands on INCOME again (income/golden-path asserts the
      // same gauge for a placed fleet) — the rebooted daemon re-derives INCOME, it does not regress to DATA.
      const phaseGauge = await pollUntil(
        () => scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' }),
        (v) => v === 1,
        { steps: 30, advanceMs: 1000 },
      );
      expect(phaseGauge, 'rebooted daemon re-derives INCOME within its first reconcile ticks').toBe(1);
    } finally {
      await daemon.stop();
    }
  }, 300_000);
});
