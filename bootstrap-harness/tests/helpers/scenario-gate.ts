import type { GateFixture } from './fixtures-gate';
import { twinGate } from './twin-admin-gate';
import { startTestDaemon, resetDaemonDb, type DaemonHandle } from './daemon';
import { launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric } from './drive';

export interface GateScenarioCtx {
  twin: typeof twinGate;
  daemon: DaemonHandle;
  launchBootstrap: typeof launchBootstrap;
  pollUntil: typeof pollUntil;
  advanceTicks: typeof advanceTicks;
  scrapeBootstrapMetric: typeof scrapeBootstrapMetric;
}

export async function withGateScenario(
  fixture: GateFixture,
  fn: (ctx: GateScenarioCtx) => Promise<void>,
): Promise<void> {
  await twinGate.seedGate(fixture); // (1) admin-seed the post-INCOME / GATE-entry world; clock frozen
  await resetDaemonDb(); // (2) wipe daemon mirror (keep players)
  const daemon = await startTestDaemon(); // (3) boot isolated daemon (re-syncs from the API)
  try {
    await fn({ twin: twinGate, daemon, launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric });
  } finally {
    await daemon.stop(); // (4) teardown; the API server stays up for the next scenario
  }
}
