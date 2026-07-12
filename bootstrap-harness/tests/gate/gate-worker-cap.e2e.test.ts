import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateEntry } from '../helpers/fixtures-gate';

describe('bootstrap GATE — worker cap', () => {
  it('never sizes more gate workers than gate_worker_target, even with many chains', async () => {
    await withGateScenario(gateEntry({ haulers: 2, gateMaterialChains: 12, credits: 6_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.gateWorkers.length >= 4, { steps: 80, advanceMs: 1000 });
      await ctx.advanceTicks(15, 1000); // the cap must hold across extra ticks
      const s = await ctx.twin.gateState();
      // gate_worker_target caps the pool well below the 12 chains.
      expect(s.gateWorkers.length).toBeLessThanOrEqual(8);
    });
  }, 240_000);
});
