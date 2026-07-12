import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateEntry } from '../helpers/fixtures-gate';
import { twinIncome } from '../helpers/twin-admin-income';

describe('bootstrap GATE — sticky phase (anti-thrash)', () => {
  it('stays GATE after construction starts even when $/hr drops below income_bar', async () => {
    await withGateScenario(gateEntry({ haulers: 4, incomePerHour: 60000, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      // Reach construction-started.
      await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.construction.started, { steps: 40, advanceMs: 1000 });
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(1);

      // Drop realized $/hr BELOW income_bar (as repurposing would) — a naive derivation would regress to INCOME.
      await twinIncome.setIncome(0);
      await ctx.advanceTicks(10, 1000);
      // Sticky-on-construction-started: phase MUST remain GATE, never INCOME.
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'GATE' })).toBe(1);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(0);
      const s = await ctx.twin.gateState();
      expect(s.construction.started).toBe(true); // still building, not thrashing back to buy haulers
    });
  }, 180_000);
});
