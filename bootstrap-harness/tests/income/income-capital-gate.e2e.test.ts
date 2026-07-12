import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap INCOME — capital gate', () => {
  it('blocks a hauler buy that breaches reserve_margin, then buys once funded', async () => {
    // 300k hauler but only 400k credits → a buy leaves 100k < 50% reserve → blocked.
    await withIncomeScenario(incomeEntry({ hubs: ['X1-PZ28-H1'], credits: 400000, haulerPrice: 300000 }), async (ctx) => {
      ctx.launchBootstrap();
      await ctx.advanceTicks(8, 1000);
      const s1 = await ctx.twin.incomeState();
      expect(countCall(s1.mutationLog, 'PurchaseShip')).toBe(0); // no hauler while under-funded
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_haulers_total')).toBe(0);

      await ctx.twin.setCredits(3_000_000); // fund → gate releases
      const s2 = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => countCall(st.mutationLog, 'PurchaseShip') >= 1,
        { steps: 20, advanceMs: 1000 },
      );
      expect(countCall(s2.mutationLog, 'PurchaseShip')).toBeGreaterThanOrEqual(1);
    });
  }, 180_000);
});
