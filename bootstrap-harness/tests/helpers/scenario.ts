import type { ResetFixture } from './fixtures';
import { twin } from './twin-admin';
import { startTestDaemon, resetDaemonDb, type DaemonHandle } from './daemon';
import { launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric } from './drive';

export interface ScenarioCtx {
  twin: typeof twin;
  daemon: DaemonHandle;
  launchBootstrap: typeof launchBootstrap;
  pollUntil: typeof pollUntil;
  advanceTicks: typeof advanceTicks;
  scrapeBootstrapMetric: typeof scrapeBootstrapMetric;
}

export async function withScenario(
  fixture: ResetFixture,
  fn: (ctx: ScenarioCtx) => Promise<void>,
): Promise<void> {
  await twin.reset(fixture); // (1) admin-seed the world; clock left frozen
  await resetDaemonDb(); // (2) wipe daemon mirror (keep players)
  const daemon = await startTestDaemon(); // (3) boot isolated daemon (re-syncs from the API)
  try {
    await fn({ twin, daemon, launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric });
  } finally {
    await daemon.stop(); // (4) teardown; the API server stays up for the next scenario
  }
}
