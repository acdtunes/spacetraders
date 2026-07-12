import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';

describe('bootstrap INCOME — golden path', () => {
  it('retires frigate → hub haulers → batch-contract → holds at INCOME-complete past income_bar', async () => {
    // Treasury ample so every staged hauler clears the capital gate; the twin derives its hubs from
    // the real marketplace topology (the coordinator ranks + places on those same real waypoints).
    await withIncomeScenario(
      incomeEntry({ credits: 3_000_000, haulerPrice: 300000 }),
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
        // The daemon's hauler COUNTER is incremented after BuyAndPlace RETURNS (its hull dedicated +
        // placed, i.e. arrived), whereas the twin sets parkedHub at navigate-ISSUE (before arrival). So
        // the counter trails the twin's parked-hauler view by the last hop's compressed travel. Sampling
        // it at the instant the twin shows 4 parked raced the counter (it read 3 — "expected 3 to be 4");
        // poll it up to 4 instead. Teeth intact: exhausting without reaching 4 fails, and an overshoot to
        // 5 would already have tripped placed.length).toBe(4) on the twin above.
        const haulersCounted = await ctx.pollUntil(
          () => ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_haulers_total'),
          (v) => v === 4,
          { steps: 40, advanceMs: 1000 },
        );
        expect(haulersCounted).toBe(4);
        expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(1);

        // Force $/hr over income_bar → derive INCOME-complete. GATE is a stub here (never activates).
        await ctx.twin.setIncome(60000);
        await ctx.pollUntil(() => ctx.twin.incomeState(), (st) => st.creditsPerHour >= 60000, { steps: 10, advanceMs: 1000 });
        expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(0);
      },
    );
  }, 240_000);
});
