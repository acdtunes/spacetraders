import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateEntry } from '../helpers/fixtures-gate';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap GATE — worker sizing', () => {
  it('buys the whole gate-delivery fleet from income (no repurpose), leaving the contract fleet intact', async () => {
    // Option B: the coordinator BUYS the entire gate-delivery fleet from contract income — it NEVER
    // repurposes the 2 seeded contract haulers, which stay contract-tagged and earning through GATE.
    // D is sized from the pipeline the coordinator ACTUALLY sees, not the fixture: the twin serves the
    // 2-line GATE_MANIFEST (FAB_MATS + ADVANCED_CIRCUITRY) on /v2 construction, so the started pipeline's
    // materials len == 2 ⇒ obs.GateMaterialChains == 2 (NOT the fixture's gateMaterialChains) ⇒
    // desired = min(2 + 1 delivery, 6) = 3. So the full complement is D = 3, ALL bought.
    await withGateScenario(gateEntry({ haulers: 2, gateMaterialChains: 4, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.gateState(),
        (st) => st.gateWorkers.length >= 3,
        { steps: 120, advanceMs: 1000 }, // the daemon now BUYS all D=3 hulls one-per-tick (real travel each)
      );
      const repurposed = s.gateWorkers.filter((w) => w.source === 'repurposed').length;
      const bought = s.gateWorkers.filter((w) => w.source === 'bought').length;
      expect(repurposed).toBe(0);           // contract fleet INTACT — nothing repurposed (Option B)
      expect(bought).toBe(3);               // the whole complement D=3 is BOUGHT from income
      expect(s.gateWorkers.length).toBe(3); // target-sized exactly to the /v2 manifest: no over/under-buy
      expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(3); // /v2 truth: real buys == D == 'bought' count

      // No over-buy: once the manifest-derived D is covered, the sizer stops — extra ticks add nothing.
      await ctx.advanceTicks(10, 1000);
      const after = await ctx.twin.gateState();
      expect(after.gateWorkers.length).toBe(3);
      expect(after.gateWorkers.filter((w) => w.source === 'repurposed').length).toBe(0);
      expect(countCall(after.mutationLog, 'PurchaseShip')).toBe(3);
    });
  }, 300_000);
});
