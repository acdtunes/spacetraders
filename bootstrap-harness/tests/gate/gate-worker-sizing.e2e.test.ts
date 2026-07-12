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
      // Only the delta is bought: 4 material chains ⇒ target 4 workers, minus 2 repurposed ⇒ 2 bought.
      // (The prior `bought === length - repurposed` was a tautology — length ≡ repurposed + bought by
      // construction — so it passed even if the sizer ignored the idle haulers and over-bought.)
      expect(bought).toBe(2);
      expect(s.gateWorkers.length).toBe(4);       // target-sized exactly: no over/under-provisioning
      expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(bought); // /v2 truth: real buys == 'bought' count
    });
  }, 240_000);
});
