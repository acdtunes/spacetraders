import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap INCOME — batch-contract launch', () => {
  it('launches the contract fleet coordinator exactly once (idempotent across ticks)', async () => {
    await withIncomeScenario(incomeEntry({ credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.batchContractRunning,
        { steps: 40, advanceMs: 1000 },
      );
      expect(s.batchContractRunning).toBe(true);
      // Run more ticks; the launch is guarded → fires at most once.
      await ctx.advanceTicks(12, 1000);
      const s2 = await ctx.twin.incomeState();
      expect(s2.batchContractRunning).toBe(true);
      expect(countCall(s2.mutationLog, 'batch-contract')).toBeLessThanOrEqual(1);
    });
  }, 180_000);
});
