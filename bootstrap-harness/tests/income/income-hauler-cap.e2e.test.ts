import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap INCOME — hauler cap', () => {
  it('buys at most hauler_target haulers even when more hubs are viable', async () => {
    // The twin serves 30 real marketplaces, so the coordinator sees FAR more than hauler_target viable
    // hubs; assert the fleet still never exceeds the cap (hauler_target defaults to 4–5). Treasury ample.
    await withIncomeScenario(incomeEntry({ credits: 5_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      // Let it buy until it plateaus (no new buy across a settle window).
      await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.haulers.filter((h) => h.parkedHub).length >= 4,
        { steps: 80, advanceMs: 1000 },
      );
      await ctx.advanceTicks(15, 1000); // extra ticks — the cap must hold
      const s = await ctx.twin.incomeState();
      const bought = countCall(s.mutationLog, 'PurchaseShip');
      expect(bought).toBeGreaterThanOrEqual(4);
      expect(bought).toBeLessThanOrEqual(5); // hauler_target ceiling (4–5), never 8
      expect(s.haulers.filter((h) => h.parkedHub).length).toBeLessThanOrEqual(5);
    });
  }, 240_000);
});
