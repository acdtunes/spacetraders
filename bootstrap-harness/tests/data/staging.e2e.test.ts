import { describe, expect, it } from 'vitest';
import { withScenario } from '../helpers/scenario';
import { coldStart } from '../helpers/fixtures';
import { ticksOf } from '../helpers/mutation-log';

describe('bootstrap DATA — staging', () => {
  it('buys at most one probe per reconcile tick (distinct world-times per buy)', async () => {
    await withScenario(coldStart({ probePrice: 40000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.state(),
        (v) => ticksOf(v.mutationLog, 'PurchaseShip').length >= 2,
        { steps: 40, advanceMs: 1000 },
      );
      const buyTimes = ticksOf(s.mutationLog, 'PurchaseShip');
      expect(buyTimes.length).toBe(2);
      // The two buys carry different world-times → they landed on different ticks (never batched).
      expect(new Set(buyTimes).size).toBe(2);
    });
  }, 120_000);
});
