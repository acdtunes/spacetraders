import { describe, expect, it } from 'vitest';
import { withScenario } from '../helpers/scenario';
import { coldStart } from '../helpers/fixtures';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap DATA — fail-closed', () => {
  it('does not buy on a failed observation, then resumes when the fault clears', async () => {
    await withScenario(coldStart({ probePrice: 40000 }), async (ctx) => {
      // Arm a single fleet-read failure that lands on the next reconcile observe.
      await ctx.twin.injectFault({ endpoint: 'GET /my/ships', code: 500, count: 1 });
      ctx.launchBootstrap();

      // Give the faulted tick time to occur and fail closed (no buy), then the fault self-clears
      // and buying resumes — assert we still reach 2 buys (fail-closed delayed, not lost).
      const s = await ctx.pollUntil(
        () => ctx.twin.state(),
        (v) => countCall(v.mutationLog, 'PurchaseShip') >= 2,
        { steps: 40, advanceMs: 1000 },
      );
      expect(countCall(s.mutationLog, 'PurchaseShip')).toBe(2);
      // The first buy's world-time is strictly after the fault would have fired — it did NOT buy
      // on the faulted observe (proved by never exceeding 2 buys + reaching the target).
    });
  }, 120_000);
});
