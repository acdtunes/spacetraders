import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';
import { ticksOf } from '../helpers/mutation-log';

describe('bootstrap INCOME — staging', () => {
  it('buys at most one hauler per reconcile tick (distinct world-times)', async () => {
    await withIncomeScenario(incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3'], credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => ticksOf(st.mutationLog, 'PurchaseShip').length >= 3,
        { steps: 80, advanceMs: 1000 },
      );
      const buyTimes = ticksOf(s.mutationLog, 'PurchaseShip');
      expect(buyTimes.length).toBe(3);
      expect(new Set(buyTimes).size).toBe(3); // each buy on a different tick — never batched
    });
  }, 240_000);
});
