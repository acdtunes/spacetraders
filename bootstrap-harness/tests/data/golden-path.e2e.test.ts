import { describe, expect, it } from 'vitest';
import { withScenario } from '../helpers/scenario';
import { coldStart } from '../helpers/fixtures';
import { countCall } from '../helpers/mutation-log';

describe('bootstrap DATA — golden path', () => {
  it('cold agent → buys 2 probes → 3 total scouting → holds at DATA-complete', async () => {
    await withScenario(coldStart({ probePrice: 40000 }), async (ctx) => {
      ctx.launchBootstrap();

      // Reconciler buys probe_target-1 = 2 probes, one per tick, at HQ (no travel).
      const bought = await ctx.pollUntil(
        () => ctx.twin.state(),
        (s) => countCall(s.mutationLog, 'PurchaseShip') >= 2,
        { steps: 40, advanceMs: 1000 },
      );

      // World truth: 3 probes, each assigned to scout-all-markets, treasury debited 2×price.
      const probes = bought.ships.filter((x) => x.role === 'SATELLITE');
      expect(probes.length).toBe(3);
      expect(probes.every((p) => p.scoutAssignment === 'scout-all-markets')).toBe(true);
      expect(bought.agent.credits).toBe(175000 - 2 * 40000);
      expect(countCall(bought.mutationLog, 'PurchaseShip')).toBe(2); // no over-buy

      // Daemon truth: probes counter = 2 (bought), phase gauge active on DATA.
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_probes_total')).toBe(2);
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'DATA' })).toBe(1);

      // Force coverage over the bar → next observation derives DATA-complete and holds.
      await ctx.twin.forceCoverage({ fraction: 0.95 });
      const done = await ctx.pollUntil(
        () => ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'DATA' }),
        (v) => v === 1, // DATA gauge stays 1 while holding at DATA-complete (INCOME is a stub)
        { steps: 40, advanceMs: 1000 },
      );
      expect(done).toBe(1);
      // Still exactly 2 buys — DATA-complete does not buy more.
      expect(countCall((await ctx.twin.state()).mutationLog, 'PurchaseShip')).toBe(2);
    });
  }, 120_000);
});
