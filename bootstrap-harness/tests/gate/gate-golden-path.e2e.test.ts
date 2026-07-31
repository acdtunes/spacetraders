import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateEntry } from '../helpers/fixtures-gate';

describe('bootstrap GATE — golden path', () => {
  it('starts construction, adopts, sizes workers, reaches COMPLETE, hands off to the autosizer, exits', async () => {
    await withGateScenario(gateEntry({ gateMaterialChains: 3, haulers: 4, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      // Construction started + executor adopted (post L57 bounce) + the gate-delivery fleet BOUGHT to D=3.
      const s = await ctx.pollUntil(
        () => ctx.twin.gateState(),
        (st) => st.construction.started && st.construction.adopted && st.gateWorkers.length >= 3,
        { steps: 120, advanceMs: 1000 }, // widened: the daemon now BUYS all D=3 hulls one-per-tick (real travel each)
      );
      expect(s.construction.site).toBeTruthy();
      expect(s.gateWorkers.length).toBeGreaterThanOrEqual(3);
      // Option B: the whole gate-delivery fleet is BOUGHT — the contract fleet is never repurposed.
      expect(s.gateWorkers.filter((w) => w.source === 'repurposed').length).toBe(0);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(1);

      // Force construction 100% → COMPLETE → hand-off launches the standing economy → coordinator exits.
      await ctx.twin.setConstruction(100);
      const done = await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.done, { steps: 60, advanceMs: 1000 });
      expect(done.autosizerRunning).toBe(true);
      expect(done.standingCoordinators.siting).toBe(true);
      expect(done.standingCoordinators.workerRebalancer).toBe(true);
      expect(done.done).toBe(true);
      // Contract fleet still intact + earning at COMPLETE: no gate worker ever came from a repurposed
      // contract hauler, so the whole contract fleet earned its way through GATE (funding the fleet buys).
      expect(done.gateWorkers.filter((w) => w.source === 'repurposed').length).toBe(0);
    });
  }, 300_000);
});
