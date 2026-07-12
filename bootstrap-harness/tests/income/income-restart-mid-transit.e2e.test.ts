import { describe, expect, it } from 'vitest';
import { twinIncome } from '../helpers/twin-admin-income';
import { incomeEntry } from '../helpers/fixtures-income';
import { resetDaemonDb, startTestDaemon } from '../helpers/daemon';
import { launchBootstrap, pollUntil, scrapeBootstrapMetric } from '../helpers/drive';
import { countCall } from '../helpers/mutation-log';

// ─── GWT ────────────────────────────────────────────────────────────────────────────
// GIVEN the bootstrap daemon has bought a hauler and dispatched it toward a hub, and the hull is
//       still EN ROUTE (its arrival timer lives only in the daemon's memory),
// WHEN  the daemon is killed mid-flight and a NEW daemon boots on the same DB + twin,
// THEN  the new daemon re-adopts that exact hull — no re-buy, no re-dispatch (same transit/arrival),
//       its arrival is acted on, it parks, and INCOME converges — identical to an uninterrupted run.
//
// Bar coverage: (a) phase re-detect → INCOME; (b) no double PurchaseShip; (c) the in-flight hull
//   survives by identity + (when the leg is a real topology transit) is ADOPTED not re-navigated;
//   (d) converges (all hubs served + batch-contract launched).
//
// This is the first spec to track a SPECIFIC mid-flight hull across the reboot; the sibling
// income-restart-idempotency only counts totals. It pairs the report-seam-free /v2 observables
// (PurchaseShip count, hull identity, nav.route.arrival) — no flag-only teeth.
//
// EXPECTED: GREEN if the coordinator re-arms arrival detection from GET /my/ships (DB/twin) for a hull
//   it did NOT dispatch this lifetime. RED — the implementation gap this spec exists to expose — if
//   arrival timers are armed only for same-lifetime navigations: the mid-flight hull is then orphaned,
//   so either convergence STALLS (pollUntil throws) or the hull is re-dispatched (route.arrival moves)
//   or re-bought (PurchaseShip > 3). See st-drm-14 report, gap #1 (coordinator arrival-timer re-arm).
//
// NOTE (topology caveat): the ADOPT-not-redispatch sub-assertion only bites when the hub leg runs
//   between two real captured waypoints (a real transit carrying nav.route). If the seeded hubs are
//   logical symbols the hull teleports (no route) and that one sub-assertion is skipped — the hard
//   teeth (identity survival, no re-buy, convergence) still hold. See report gap #4.
describe('bootstrap INCOME — restart mid-TRANSIT (in-flight hull re-adoption)', () => {
  it('re-adopts a hauler that was in flight at the kill: arrival acted on, parked, never re-bought', async () => {
    await twinIncome.seedIncome(incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3'], credits: 3_000_000 }));
    await resetDaemonDb();
    // Widen the IN_TRANSIT window: at the orchestrator's 200x a >=15s real leg resolves in ~50-300ms
    // (uncatchable). 6x stretches a real hub leg to ~2.5-7s so the kill lands while the hull is in
    // flight. STICKY across reset → restored to the fast default in finally.
    await twinIncome.setCompression(6);

    let daemon = await startTestDaemon();
    try {
      launchBootstrap();
      // Lifetime 1: stop the instant the FIRST hauler has been bought AND a navigate has been issued.
      const afterFirst = await pollUntil(
        () => twinIncome.incomeState(),
        (s) => countCall(s.mutationLog, 'PurchaseShip') >= 1 && countCall(s.mutationLog, 'navigate') >= 1,
        { steps: 60, stepMs: 300, advanceMs: 1000 },
      );
      expect(countCall(afterFirst.mutationLog, 'PurchaseShip')).toBe(1);
      const firstHauler = afterFirst.haulers[0];
      expect(firstHauler).toBeDefined();
      const symbol = firstHauler.symbol;
      // Capture the transit exactly as the OLD daemon minted it. Present only when the hub leg runs
      // between real topology waypoints; a logical-hub hop teleports and carries no route (null).
      const shipBefore = afterFirst.ships.find((x) => x.symbol === symbol);
      const arrivalBefore = shipBefore?.nav.route?.arrival ?? null;

      // Kill the daemon while the hull is still en route — its in-memory arrival timer dies with it.
      await daemon.stop();
      await twinIncome.setCompression(200); // remainder converges fast; the in-flight leg keeps its slow arrival
      daemon = await startTestDaemon(); // reboot: same DB + twin; the hull + its transit persist in the twin

      launchBootstrap();
      const done = await pollUntil(
        () => twinIncome.incomeState(),
        (s) => s.haulers.filter((h) => h.parkedHub).length >= 3 && s.batchContractRunning,
        { steps: 80, advanceMs: 1000 },
      );

      // (b) exactly 3 buys across BOTH lifetimes — the in-flight hull was NOT re-bought.
      expect(countCall(done.mutationLog, 'PurchaseShip')).toBe(3);
      // (c) the SAME hull (by identity, not a count) survived the reboot and completed its assignment.
      expect(done.ships.filter((x) => x.symbol === symbol).length).toBe(1); // not duplicated
      const survived = done.haulers.find((h) => h.symbol === symbol);
      expect(survived).toBeDefined();
      expect(survived?.parkedHub).toBeTruthy(); // the rebooted daemon drove the adopted hull to a hub
      // (c) ADOPT-not-redispatch: with a real in-flight transit at the kill, the arrival instant the
      // hull finally resolves on is the ORIGINAL — the new daemon adopted the transit, it did not
      // supersede it with a fresh navigate (which would re-mint a strictly later arrival).
      if (arrivalBefore) {
        const arrivedShip = done.ships.find((x) => x.symbol === symbol);
        expect(arrivedShip?.nav.route?.arrival).toBe(arrivalBefore);
      }
      // (a) phase re-detection + (d) convergence to distinct hubs.
      expect(await scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(1);
      expect(new Set(done.haulers.filter((h) => h.parkedHub).map((h) => h.parkedHub)).size).toBe(3);
    } finally {
      await daemon.stop();
      await twinIncome.setCompression(200); // never leak the slow clock to a later spec (compression is sticky)
    }
  }, 300_000);
});
