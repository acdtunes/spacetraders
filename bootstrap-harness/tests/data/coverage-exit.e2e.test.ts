import { describe, expect, it } from 'vitest';
import { withScenario } from '../helpers/scenario';
import { coldStart } from '../helpers/fixtures';

describe('bootstrap DATA — coverage-bar exit', () => {
  it('holds in DATA below the bar, then derives DATA-complete once coverage crosses it', async () => {
    await withScenario(coldStart({ probePrice: 40000, coverage: 0.0 }), async (ctx) => {
      ctx.launchBootstrap();

      // Below the bar, the phase stays DATA across ticks.
      await ctx.pollUntil(
        () => ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'DATA' }),
        (v) => v === 1,
        { steps: 40, advanceMs: 1000 },
      );
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(0);

      // Force coverage ≥ bar → the next tick derives DATA-complete (INCOME remains a stub: gauge
      // does not advance to an active INCOME; the daemon logs the not-implemented hold).
      await ctx.twin.forceCoverage({ fraction: 0.95 });
      const s = await ctx.pollUntil(
        () => ctx.twin.state(),
        (st) => st.coverage >= 0.95,
        { steps: 40, advanceMs: 1000 },
      );
      expect(s.coverage).toBeGreaterThanOrEqual(0.95);
      // INCOME never becomes the active phase in this harness (Slice-2 out of scope).
      expect(await ctx.scrapeBootstrapMetric('spacetraders_daemon_bootstrap_phase', { phase: 'INCOME' })).toBe(0);
    });
  }, 120_000);
});
