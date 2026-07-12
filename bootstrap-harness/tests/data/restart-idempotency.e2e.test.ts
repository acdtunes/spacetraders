import { describe, expect, it } from 'vitest';
import { twin } from '../helpers/twin-admin';
import { coldStart } from '../helpers/fixtures';
import { resetDaemonDb, startTestDaemon } from '../helpers/daemon';
import { launchBootstrap, pollUntil, scrapeBootstrapMetric } from '../helpers/drive';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap DATA — restart idempotency', () => {
  it('no double-buy when the daemon is killed mid-purchase and rebooted', async () => {
    await twin.reset(coldStart({ probePrice: 40000 }));
    await resetDaemonDb();

    // Lifetime 1: run until exactly the FIRST probe purchase is recorded, then freeze progress.
    let daemon = await startTestDaemon();
    launchBootstrap();
    const afterFirst = await pollUntil(
      () => twin.state(),
      (s) => countCall(s.mutationLog, 'PurchaseShip') >= 1,
      { steps: 40, advanceMs: 1000 },
    );
    expect(countCall(afterFirst.mutationLog, 'PurchaseShip')).toBe(1); // one buy so far
    expect(afterFirst.ships.filter((x) => x.role === 'SATELLITE').length).toBe(2); // probe really exists

    // Kill the daemon BEFORE it re-observes — the twin world (2 probes) persists; the daemon keeps
    // no in-memory progress cursor, so a reboot must re-derive "need 1 more", not "need 2".
    await daemon.stop();
    daemon = await startTestDaemon(); // reboot; SAME test DB (operation record persists), SAME twin
    try {
      launchBootstrap();
      const done = await pollUntil(
        () => twin.state(),
        (s) => s.ships.filter((x) => x.role === 'SATELLITE').length >= 3,
        { steps: 40, advanceMs: 1000 },
      );
      // The crux: exactly probe_target-1 = 2 PurchaseShip across BOTH daemon lifetimes — no re-buy
      // of the probe that existed at restart.
      expect(countCall(done.mutationLog, 'PurchaseShip')).toBe(2);
      expect(done.ships.filter((x) => x.role === 'SATELLITE').length).toBe(3);
      // (a) phase re-detection after the reboot lands on DATA again — the rebooted daemon re-derives
      // its phase from DB+twin, it does not thrash to a different phase.
      expect(await scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'DATA' })).toBe(1);
      // (b) treasury debited EXACTLY twice (probePrice 40000) across BOTH daemon lifetimes — identical
      // to a single uninterrupted run (data/golden-path asserts the same 175000-2*40000). An independent
      // /v2 observable (agent.credits) pairing the PurchaseShip count: a re-buy would show 55000, not 95000.
      expect(done.agent.credits).toBe(175000 - 2 * 40000);
    } finally {
      await daemon.stop();
    }
  }, 180_000);
});
