import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateEntry } from '../helpers/fixtures-gate';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap GATE — worker sizing', () => {
  it('repurposes idle haulers first, then buys only the delta to the chain count', async () => {
    // 2 idle haulers, 4 material chains → repurpose 2, buy ~2 more (delta), keep min_contract_earners.
    await withGateScenario(gateEntry({ haulers: 2, gateMaterialChains: 4, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.gateState(),
        (st) => st.gateWorkers.length >= 4,
        { steps: 80, advanceMs: 1000 },
      );
      const repurposed = s.gateWorkers.filter((w) => w.source === 'repurposed').length;
      const bought = s.gateWorkers.filter((w) => w.source === 'bought').length;
      expect(repurposed).toBe(2);                 // both idle haulers repurposed first
      expect(bought).toBe(s.gateWorkers.length - repurposed); // only the delta bought
      expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(bought);
    });
  }, 240_000);
});
