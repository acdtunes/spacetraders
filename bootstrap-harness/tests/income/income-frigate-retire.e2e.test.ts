import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap INCOME — frigate retirement', () => {
  it('clears the frigate contract dedication exactly once', async () => {
    await withIncomeScenario(incomeEntry({ hubs: ['X1-PZ28-H1'], credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.frigateContractTagged === false,
        { steps: 30, advanceMs: 1000 },
      );
      expect(s.frigateContractTagged).toBe(false);
      // Idempotent: run more ticks; the retire (fleet-unassign) fires at most once.
      await ctx.advanceTicks(10, 1000);
      const s2 = await ctx.twin.incomeState();
      expect(countCall(s2.mutationLog, 'fleet-unassign')).toBeLessThanOrEqual(1);
      expect(s2.frigateContractTagged).toBe(false);
    });
  }, 180_000);
});
