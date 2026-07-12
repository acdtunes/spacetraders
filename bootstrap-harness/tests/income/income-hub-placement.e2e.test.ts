import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';

describe('bootstrap INCOME — hub placement', () => {
  it('places one hauler on each of the ranked contract hubs (no doubling up)', async () => {
    const hubs = ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3'];
    await withIncomeScenario(incomeEntry({ hubs, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.haulers.filter((h) => h.parkedHub).length >= 3,
        { steps: 60, advanceMs: 1000 },
      );
      const placedHubs = s.haulers.map((h) => h.parkedHub).filter(Boolean) as string[];
      // Exactly one hauler per hub, and every hub used is one of the ranked hubs.
      expect(new Set(placedHubs).size).toBe(placedHubs.length);          // no hub doubled
      expect(placedHubs.every((w) => s.hubs.includes(w))).toBe(true);    // placed on ranked hubs
      expect(placedHubs.length).toBe(3);
    });
  }, 180_000);
});
