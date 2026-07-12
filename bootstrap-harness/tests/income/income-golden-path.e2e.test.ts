import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';

describe('bootstrap INCOME — golden path', () => {
  it('retires frigate → hub haulers → batch-contract → holds at INCOME-complete past income_bar', async () => {
    // 4 hubs, treasury ample so all clear the capital gate.
    await withIncomeScenario(
      incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3', 'X1-PZ28-H4'], credits: 3_000_000, haulerPrice: 300000 }),
      async (ctx) => {
        ctx.launchBootstrap();
        const s = await ctx.pollUntil(
          () => ctx.twin.incomeState(),
          (st) => st.haulers.filter((h) => h.parkedHub).length >= 4 && st.batchContractRunning,
          { steps: 80, advanceMs: 1000 },
        );
        expect(s.frigateContractTagged).toBe(false);                       // frigate retired
        const placed = s.haulers.filter((h) => h.parkedHub);
        expect(placed.length).toBe(4);                                     // one per hub
        expect(new Set(placed.map((h) => h.parkedHub)).size).toBe(4);      // distinct hubs
        expect(s.batchContractRunning).toBe(true);                         // earning
        expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_haulers_total')).toBe(4);
        expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(1);

        // Force $/hr over income_bar → derive INCOME-complete. GATE is a stub here (never activates).
        await ctx.twin.setIncome(60000);
        await ctx.pollUntil(() => ctx.twin.incomeState(), (st) => st.creditsPerHour >= 60000, { steps: 10, advanceMs: 1000 });
        expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(0);
      },
    );
  }, 240_000);
});
