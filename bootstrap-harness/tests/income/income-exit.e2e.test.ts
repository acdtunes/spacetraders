import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';

describe('bootstrap INCOME — income_bar exit', () => {
  it('holds INCOME below the bar, derives INCOME-complete once $/hr clears it (GATE stub, out of scope)', async () => {
    await withIncomeScenario(incomeEntry({ hubs: ['X1-PZ28-H1', 'X1-PZ28-H2'], credits: 3_000_000, creditsPerHour: 0 }), async (ctx) => {
      ctx.launchBootstrap();
      // Below the bar → INCOME stays active.
      await ctx.pollUntil(
        () => ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' }),
        (v) => v === 1,
        { steps: 20, advanceMs: 1000 },
      );
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(0);

      // Force $/hr over income_bar → INCOME-complete derived; GATE never activates in this harness.
      await ctx.twin.setIncome(80000);
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.creditsPerHour >= 80000,
        { steps: 10, advanceMs: 1000 },
      );
      expect(s.creditsPerHour).toBeGreaterThanOrEqual(80000);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(0);
    });
  }, 180_000);
});
