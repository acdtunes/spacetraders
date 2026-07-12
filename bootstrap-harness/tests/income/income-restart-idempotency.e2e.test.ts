import { describe, expect, it } from 'vitest';
import { twinIncome } from '../helpers/twin-admin-income';
import { incomeEntry } from '../helpers/fixtures-income';
import { resetDaemonDb, startTestDaemon } from '../helpers/daemon';
import { launchBootstrap, pollUntil } from '../helpers/drive';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap INCOME — restart idempotency', () => {
  it('no double-buy / no re-retire / no double batch-contract across a mid-purchase restart', async () => {
    await twinIncome.seedIncome(incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3'], credits: 3_000_000 }));
    await resetDaemonDb();

    // Lifetime 1: run until the FIRST hauler purchase is recorded, then stop before the next observe.
    let daemon = await startTestDaemon();
    launchBootstrap();
    const afterFirst = await pollUntil(
      () => twinIncome.incomeState(),
      (s) => countCall(s.mutationLog, 'PurchaseShip') >= 1,
      { steps: 60, advanceMs: 1000 },
    );
    expect(countCall(afterFirst.mutationLog, 'PurchaseShip')).toBe(1);
    const retiresBefore = countCall(afterFirst.mutationLog, 'fleet-unassign');
    const batchBefore = countCall(afterFirst.mutationLog, 'batch-contract');

    await daemon.stop();
    daemon = await startTestDaemon(); // reboot; same test DB + twin world (1 hauler persists)
    try {
      launchBootstrap();
      const done = await pollUntil(
        () => twinIncome.incomeState(),
        (s) => s.haulers.filter((h) => h.parkedHub).length >= 3,
        { steps: 60, advanceMs: 1000 },
      );
      // Exactly 3 hauler buys across BOTH lifetimes — the mid-flight hauler is not re-bought.
      expect(countCall(done.mutationLog, 'PurchaseShip')).toBe(3);
      // Frigate retired at most once total; batch-contract launched at most once total.
      expect(countCall(done.mutationLog, 'fleet-unassign')).toBeLessThanOrEqual(Math.max(1, retiresBefore));
      expect(countCall(done.mutationLog, 'batch-contract')).toBeLessThanOrEqual(Math.max(1, batchBefore, 1));
      expect(done.frigateContractTagged).toBe(false);
    } finally {
      await daemon.stop();
    }
  }, 300_000);
});
