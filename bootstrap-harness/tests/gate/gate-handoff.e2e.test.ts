import { describe, expect, it } from 'vitest';
import { withGateScenario } from '../helpers/scenario-gate';
import { gateEntry } from '../helpers/fixtures-gate';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap GATE — hand-off', () => {
  it('at COMPLETE launches autosizer + standing coordinators exactly once', async () => {
    await withGateScenario(gateEntry({ constructionPercent: 95, credits: 3_000_000 }), async (ctx) => {
      ctx.launchBootstrap();
      await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.construction.started, { steps: 40, advanceMs: 1000 });
      await ctx.twin.setConstruction(100);
      const s = await ctx.pollUntil(() => ctx.twin.gateState(), (st) => st.done, { steps: 30, advanceMs: 1000 });
      expect(s.autosizerRunning).toBe(true);
      expect(s.standingCoordinators.siting).toBe(true);
      expect(s.standingCoordinators.workerRebalancer).toBe(true);
      // Extra ticks after COMPLETE must NOT relaunch anything (guarded, exactly-once).
      await ctx.advanceTicks(10, 1000);
      const s2 = await ctx.twin.gateState();
      expect(countCall(s2.mutationLog, 'launch-autosizer')).toBe(1);
      expect(countCall(s2.mutationLog, 'launch-siting')).toBeLessThanOrEqual(1);
      expect(countCall(s2.mutationLog, 'launch-worker-rebalancer')).toBeLessThanOrEqual(1);
    });
  }, 180_000);
});
