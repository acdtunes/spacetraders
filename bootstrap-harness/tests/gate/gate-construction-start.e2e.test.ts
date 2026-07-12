import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateEntry } from '../helpers/fixtures-gate';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap GATE — construction start + adoption bounce', () => {
  it('starts the pipeline and bounces the executor to adopt it, each exactly once', async () => {
    await withGateScenario(gateEntry({ credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      const s = await ctx.pollUntil(
        () => ctx.twin.gateState(),
        (st) => st.construction.started && st.construction.adopted,
        { steps: 40, advanceMs: 1000 },
      );
      expect(s.construction.started).toBe(true);
      expect(s.construction.adopted).toBe(true);
      // Idempotent: extra ticks must not re-start construction or re-bounce the (already-adopted) executor.
      await ctx.advanceTicks(12, 1000);
      const s2 = await ctx.twin.gateState();
      expect(countCall(s2.mutationLog, 'construction-start')).toBeLessThanOrEqual(1);
      expect(countCall(s2.mutationLog, 'executor-bounce')).toBeLessThanOrEqual(1);
    });
  }, 180_000);
});
