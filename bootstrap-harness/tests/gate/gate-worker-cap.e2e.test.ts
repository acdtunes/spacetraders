import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateEntry } from '../helpers/fixtures-gate';

describe('bootstrap GATE — worker cap', () => {
  it('never sizes more gate workers than gate_worker_target, even with many chains', async () => {
    await withGateScenario(gateEntry({ haulers: 2, gateMaterialChains: 12, credits: 6_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.gateWorkers.length >= 3, { steps: 100, advanceMs: 1000 });
      await ctx.advanceTicks(15, 1000); // the cap must hold across extra ticks
      const s = await ctx.twin.gateState();
      // gate_worker_target caps the pool well below any inflated chain count — and because the coordinator
      // sizes to the /v2 GATE_MANIFEST it actually reads (2 chains ⇒ D=3), the fixture's 12 chains never
      // balloon the pool. Comfortably under the 8 cap.
      expect(s.gateWorkers.length).toBeLessThanOrEqual(8);
      // Option B: the whole complement is BOUGHT from income — nothing repurposed, so the contract fleet
      // is left intact and earning.
      expect(s.gateWorkers.filter((w) => w.source === 'repurposed').length).toBe(0);
      expect(s.gateWorkers.every((w) => w.source === 'bought')).toBe(true);
    });
  }, 300_000);
});
