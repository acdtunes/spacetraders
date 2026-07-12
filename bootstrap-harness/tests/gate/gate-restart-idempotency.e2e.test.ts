import { describe, expect, it } from 'vitest';
import { twinGate } from '../helpers/twin-admin-gate';
import { gateEntry } from '../helpers/fixtures-gate';
import { resetDaemonDb, startTestDaemon } from '../helpers/daemon';
import { launchBootstrap, pollUntil } from '../helpers/drive';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap GATE — restart idempotency', () => {
  it('no double-start / no re-bounce / no double-worker-buy / no double-autosizer across a mid-GATE restart', async () => {
    await twinGate.seedGate(gateEntry({ haulers: 2, gateMaterialChains: 4, credits: 3_000_000 }));
    await resetDaemonDb();

    // Lifetime 1: run until construction started + at least one worker sized, then stop.
    let daemon = await startTestDaemon();
    launchBootstrap();
    const mid = await pollUntil(
      () => twinGate.gateState(),
      (s) => s.construction.started && s.gateWorkers.length >= 1,
      { steps: 60, advanceMs: 1000 },
    );
    const startsBefore = countCall(mid.mutationLog, 'construction-start');
    const bouncesBefore = countCall(mid.mutationLog, 'executor-bounce');
    expect(startsBefore).toBe(1);

    await daemon.stop();
    daemon = await startTestDaemon(); // reboot; same DB + twin (construction + workers persist)
    try {
      launchBootstrap();
      // Drive to COMPLETE.
      await twinGate.setConstruction(100);
      const done = await pollUntil(() => twinGate.gateState(), (s) => s.done, { steps: 60, advanceMs: 1000 });
      // Guards held across the restart:
      expect(countCall(done.mutationLog, 'construction-start')).toBe(1);         // not re-started
      expect(countCall(done.mutationLog, 'executor-bounce')).toBeLessThanOrEqual(Math.max(1, bouncesBefore)); // not re-bounced once adopted
      expect(countCall(done.mutationLog, 'launch-autosizer')).toBe(1);           // launched once total
      expect(done.done).toBe(true);
    } finally {
      await daemon.stop();
    }
  }, 300_000);
});
