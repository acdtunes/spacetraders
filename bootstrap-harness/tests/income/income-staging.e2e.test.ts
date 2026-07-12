import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';
import { ticksOf } from '../helpers/mutation-log';

describe('bootstrap INCOME — staging', () => {
  it('buys at most one hauler per reconcile tick (distinct world-times)', async () => {
    // No hubs passed → the twin derives its hub set from the real marketplace topology; the coordinator
    // staged-buys one hauler per reconcile tick up to hauler_target (4). Teeth: one buy per distinct tick.
    await withIncomeScenario(incomeEntry({ credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => ticksOf(st.mutationLog, 'PurchaseShip').length >= 4,
        { steps: 80, advanceMs: 1000 },
      );
      const buyTimes = ticksOf(s.mutationLog, 'PurchaseShip');
      expect(buyTimes.length).toBe(4);
      expect(new Set(buyTimes).size).toBe(4); // each buy on a different tick — never batched
    });
  }, 240_000);
});
