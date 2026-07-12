import { describe, expect, it } from 'vitest';
import { withScenario } from '../helpers/scenario';
import { coldStart } from '../helpers/fixtures';

describe('bootstrap DATA — dry-run', () => {
  it('evaluates but mutates nothing under --dry-run', async () => {
    await withScenario(coldStart({ probePrice: 40000 }), async (ctx) => {
      ctx.launchBootstrap(['--dry-run']);
      // Advance several ticks; the world must stay byte-identical (no buys, no navigates).
      await ctx.advanceTicks(8, 1000);
      const s = await ctx.twin.state();
      expect(s.mutationLog).toEqual([]);            // nothing mutated
      expect(s.ships.filter((x) => x.role === 'SATELLITE').length).toBe(1); // still the original probe
      expect(s.agent.credits).toBe(175000);         // treasury untouched
    });
  }, 120_000);
});
