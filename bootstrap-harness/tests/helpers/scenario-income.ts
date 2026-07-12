import type { IncomeFixture } from './fixtures-income';
import { twinIncome } from './twin-admin-income';
import { startTestDaemon, resetDaemonDb, type DaemonHandle } from './daemon';
import { launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric } from './drive';

export interface IncomeScenarioCtx {
  twin: typeof twinIncome;
  daemon: DaemonHandle;
  launchBootstrap: typeof launchBootstrap;
  pollUntil: typeof pollUntil;
  advanceTicks: typeof advanceTicks;
  scrapeBootstrapMetric: typeof scrapeBootstrapMetric;
}

export async function withIncomeScenario(
  fixture: IncomeFixture,
  fn: (ctx: IncomeScenarioCtx) => Promise<void>,
): Promise<void> {
  await twinIncome.seedIncome(fixture); // (1) admin-seed the post-DATA / INCOME-entry world; clock frozen
  await resetDaemonDb(); // (2) wipe daemon mirror (keep players)
  const daemon = await startTestDaemon(); // (3) boot isolated daemon (re-syncs from the API)
  try {
    await fn({ twin: twinIncome, daemon, launchBootstrap, pollUntil, advanceTicks, scrapeBootstrapMetric });
  } finally {
    await daemon.stop(); // (4) teardown; the API server stays up for the next scenario
  }
}
