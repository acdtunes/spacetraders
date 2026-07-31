import { describe, expect, it } from 'vitest';
import { withIncomeScenario } from '../helpers/scenario-income';
import { incomeEntry } from '../helpers/fixtures-income';

describe('bootstrap INCOME — hub placement', () => {
  it('places one hauler on each of the ranked contract hubs (no doubling up)', async () => {
    // No hubs passed → the twin derives its hub set from the real marketplace topology (30 markets); the
    // coordinator ranks + places one hauler per hub up to hauler_target (4). Teeth: distinct real hubs.
    await withIncomeScenario(incomeEntry({ credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.incomeState(),
        (st) => st.haulers.filter((h) => h.parkedHub).length >= 4,
        { steps: 80, advanceMs: 1000 },
      );
      const placedHubs = s.haulers.map((h) => h.parkedHub).filter(Boolean) as string[];
      // Exactly one hauler per hub, and every hub used is a real ranked marketplace (∈ s.hubs).
      expect(new Set(placedHubs).size).toBe(placedHubs.length);          // no hub doubled
      expect(placedHubs.every((w) => s.hubs.includes(w))).toBe(true);    // placed on real marketplace hubs
      expect(placedHubs.length).toBe(4);
    });
  }, 180_000);
});
