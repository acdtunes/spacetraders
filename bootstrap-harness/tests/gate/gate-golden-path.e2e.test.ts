import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateEntry } from '../helpers/fixtures-gate';

describe('bootstrap GATE — golden path', () => {
  it('starts construction, adopts, sizes workers, reaches COMPLETE, hands off to the autosizer, exits', async () => {
    await withGateScenario(gateEntry({ gateMaterialChains: 3, haulers: 4, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      // Construction started + executor adopted (post L57 bounce) + workers sized.
      const s = await ctx.pollUntil(
        () => ctx.twin.gateState(),
        (st) => st.construction.started && st.construction.adopted && st.gateWorkers.length >= 3,
        { steps: 80, advanceMs: 1000 },
      );
      expect(s.construction.site).toBeTruthy();
      expect(s.gateWorkers.length).toBeGreaterThanOrEqual(3);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(1);

      // Force construction 100% → COMPLETE → hand-off launches the standing economy → coordinator exits.
      await ctx.twin.setConstruction(100);
      const done = await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.done, { steps: 40, advanceMs: 1000 });
      expect(done.autosizerRunning).toBe(true);
      expect(done.standingCoordinators.siting).toBe(true);
      expect(done.standingCoordinators.workerRebalancer).toBe(true);
      expect(done.done).toBe(true);
    });
  }, 240_000);
});
