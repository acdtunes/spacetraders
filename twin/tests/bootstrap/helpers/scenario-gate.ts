import type { GateFixture } from './gate-fixtures';
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
  await twinGate.seedGate(fixture);
  await resetDaemonDb();
  const daemon = await startTestDaemon();
  try {
    await fn({ twin: twinGate, daemon, launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric });
  } finally {
    await daemon.stop();
  }
}
