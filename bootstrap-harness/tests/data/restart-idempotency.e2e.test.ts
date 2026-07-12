import { describe, expect, it } from 'vitest';
import { twin } from '../helpers/twin-admin';
import { coldStart } from '../helpers/fixtures';
import { resetDaemonDb, startTestDaemon } from '../helpers/daemon';
import { launchBootstrap, pollUntil } from '../helpers/drive';
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
    } finally {
      await daemon.stop();
    }
  }, 180_000);
});
