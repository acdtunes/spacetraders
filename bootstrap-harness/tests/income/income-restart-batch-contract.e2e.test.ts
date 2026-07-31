import { describe, expect, it } from 'vitest';
import { twinIncome } from '../helpers/twin-admin-income';
import { incomeEntry } from '../helpers/fixtures-income';
import { resetDaemonDb, startTestDaemon } from '../helpers/daemon';
import { seedDaemonMarketCoverage } from '../helpers/daemon-seed';
import { launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric } from '../helpers/drive';
import { countCall } from '../helpers/mutation-log';

// INCOME ramp bounds, mirroring the daemon's compiled defaults (run_bootstrap_coordinator.go
// defaultHaulerTarget=4) and the fixture's LIGHT_HAULER price lever. They bound the reboot's
// legitimate treasury spend so the "not double-charged" tooth needs no magic number.
const HAULER_TARGET = 4;
const HAULER_PRICE = 300_000;      // incomeEntry() default SHIP_LIGHT_HAULER purchase lever
const STARTING_CREDITS = 3_000_000;
const REFUEL_SLACK = 50_000;       // absorbs the fleet's small per-hop refuels; << one hauler price

// ─── GWT ────────────────────────────────────────────────────────────────────────────
// GIVEN the bootstrap daemon has already launched the batch-contract fleet coordinator,
// WHEN  the daemon is killed AFTER that launch and a NEW daemon boots on the same DB + twin,
// THEN  the batch-contract coordinator is NOT relaunched, the treasury is not double-charged, no
//       spurious fleet churn fires, and INCOME still holds — identical to an uninterrupted run.
//
// Bar coverage: (a) phase re-detect → INCOME; (b) no duplicated side effects — the batch-contract
//   report-seam op fires exactly once ACROSS the restart, PAIRED with independent /v2 observables
//   (agent.credits treasury + PurchaseShip count), per the mandate that a flag alone proves nothing;
//   (e) is exercised by the sibling gate double-restart spec.
//
// EXPECTED: GREEN. batch-contract is an exactly-once report-seam op (twin flips its flag once; a repeat
//   report after a reboot is a pure no-op), and the twin's ONE-active-contract guard (world/contracts.ts
//   negotiate → 4511) blocks a second accept, so credits cannot be double-credited. The restart cannot
//   manufacture a second launch or a double payment.
//
// OBSERVABILITY GAP (st-drm-14 report, gap #2): the twin's contract state machine (accept/deliver/
//   fulfill) mutates world.contracts + credits but is ABSENT from both the mutationLog AND GET
//   /_twin/state. So "no re-accept / no double-deliver / fulfillment exactly-once / treasury-EXACT"
//   cannot be asserted here — only launch-idempotence + a BOUNDED treasury (every credit spent is
//   accounted for by the observed capped hull ramp, so a reboot cannot double-charge). Upgrading this spec
//   to the full class-2 bar needs a contracts view (activeContractId, accepted/fulfilled, per-line
//   unitsFulfilled) or contract mutationLog entries. That is a twin change, out of this task's scope.
describe('bootstrap INCOME — restart after batch-contract launch', () => {
  it('does not relaunch the contract fleet or double-charge the treasury across the reboot', async () => {
    await twinIncome.seedIncome(incomeEntry({ credits: STARTING_CREDITS }));
    await resetDaemonDb();
    await seedDaemonMarketCoverage(); // DATA-complete coverage in the daemon's local DB (persists across
    // the reboot below — resetDaemonDb is NOT re-run) so both lifetimes derive INCOME, not DATA.

    let daemon = await startTestDaemon();
    try {
      launchBootstrap();
      // Lifetime 1: run until the batch-contract coordinator is launched.
      const launched = await pollUntil(
        () => twinIncome.incomeState(),
        (s) => s.batchContractRunning,
        { steps: 60, advanceMs: 1000 },
      );
      expect(countCall(launched.mutationLog, 'batch-contract')).toBe(1);

      await daemon.stop();
      daemon = await startTestDaemon(); // reboot: same DB + twin; batchContractRunning flag persists

      launchBootstrap();
      // The rebooted daemon must re-derive INCOME and see the contract fleet already running.
      const back = await pollUntil(
        () => twinIncome.incomeState(),
        (s) => s.batchContractRunning,
        { steps: 40, advanceMs: 1000 },
      );
      expect(back.batchContractRunning).toBe(true);
      // Stress idempotence: extra reconcile ticks must not re-fire the launch nor churn the fleet.
      await advanceTicks(12, 1000);
      const done = await twinIncome.incomeState();

      // (b) the launch is exactly-once ACROSS the restart — not re-fired by the reboot.
      expect(countCall(done.mutationLog, 'batch-contract')).toBe(1);
      expect(done.batchContractRunning).toBe(true);
      // (b) independent /v2 observables pairing the flag. The INCOME hauler ramp is ACTIVE here (the
      // daemon-coverage seed makes BOTH lifetimes derive INCOME), so haulers ARE bought AFTER the
      // batch-contract launch — the reboot must RESUME that ramp (recognising the hulls already bought
      // + dedicated), never RESTART it. Two independent teeth prove no double-charge without depending
      // on the (unobservable) contract-income machine: total purchases never exceed the hull cap (a
      // double-buy of a hull that existed at reboot would push it past HAULER_TARGET), and every credit
      // the treasury lost is accounted for by exactly those observed purchases (+ minor refuels).
      const purchases = countCall(done.mutationLog, 'PurchaseShip');
      expect(purchases).toBeLessThanOrEqual(HAULER_TARGET);
      expect(STARTING_CREDITS - done.agent.credits).toBeLessThanOrEqual(purchases * HAULER_PRICE + REFUEL_SLACK);
      // No re-retire of the frigate across the restart.
      expect(countCall(done.mutationLog, 'fleet-unassign')).toBeLessThanOrEqual(1);
      // (a) phase re-detection lands on INCOME again (EVENTUAL — poll: the gauge appears on the
      // recovered brain's first reconcile tick).
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
