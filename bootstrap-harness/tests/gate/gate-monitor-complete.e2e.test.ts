import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateEntry } from '../helpers/fixtures-gate';

describe('bootstrap GATE — monitor to COMPLETE', () => {
  it('holds GATE below 100%, derives COMPLETE at 100%', async () => {
    await withGateScenario(gateEntry({ constructionPercent: 40, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.construction.started, { steps: 40, advanceMs: 1000 });
      // At 40% it holds GATE, not done.
      await ctx.advanceTicks(6, 1000);
      let s = await ctx.twin.gateState();
      expect(s.done).toBe(false);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(1);
      // Force 100% → COMPLETE.
      await ctx.twin.setConstruction(100);
      s = await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.done, { steps: 30, advanceMs: 1000 });
      expect(s.done).toBe(true);
      expect(s.construction.percent).toBeGreaterThanOrEqual(100);
    });
  }, 180_000);
});
