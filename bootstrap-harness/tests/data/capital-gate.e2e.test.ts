import { describe, expect, it } from 'vitest';
import { withScenario } from '../helpers/scenario';
import { coldStart } from '../helpers/fixtures';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap DATA — capital gate', () => {
  it('blocks a buy that would exceed reserve_margin×treasury, then releases when funded', async () => {
    // probePrice 40k but only 60k credits: a 40k buy leaves 20k < 50% reserve → blocked.
    await withScenario(coldStart({ credits: 60000, probePrice: 40000 }), async (ctx) => {
      ctx.launchBootstrap();

      // Over a bounded budget the money-guard buys nothing.
      await ctx.advanceTicks(8, 1000);
      const s1 = await ctx.twin.state();
      expect(countCall(s1.mutationLog, 'PurchaseShip')).toBe(0);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_probes_total')).toBe(0);

      // Fund the treasury; the gate releases and a buy occurs.
      await ctx.twin.setCredits(600000);
      const funded = await ctx.pollUntil(
        () => ctx.twin.state(),
        (s) => countCall(s.mutationLog, 'PurchaseShip') >= 1,
        { steps: 12, advanceMs: 1000 },
      );
      expect(countCall(funded.mutationLog, 'PurchaseShip')).toBeGreaterThanOrEqual(1);
    });
  }, 120_000);
});
