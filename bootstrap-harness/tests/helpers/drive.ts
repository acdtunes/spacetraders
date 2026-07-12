import { runCli, METRICS_URL, type RunCliResult } from './config';
import { twin } from './twin-admin';
import { parseMetric } from './parse-metrics';

export function launchBootstrap(flags: string[] = []): RunCliResult {
  return runCli(['workflow', 'bootstrap', '--player-id', '1', ...flags]);
}

export async function scrapeBootstrapMetric(
  name: string,
  labels?: Record<string, string>,
): Promise<number | null> {
  const res = await fetch(METRICS_URL);
  if (!res.ok) throw new Error(`GET ${METRICS_URL} → ${res.status}`);
  return parseMetric(await res.text(), name, labels);
}

export async function pollUntil<T>(
  fn: () => Promise<T>,
  pred: (v: T) => boolean,
  opts: { steps?: number; stepMs?: number; advanceMs?: number } = {},
): Promise<T> {
  const steps = opts.steps ?? 30;
  const stepMs = opts.stepMs ?? 300; // real wall gap between daemon reconcile observations
  const advanceMs = opts.advanceMs ?? 0; // twin world-time advanced each step (0 = don't advance)
  let last: T = await fn();
  for (let i = 0; i < steps; i++) {
    if (pred(last)) return last;
    if (advanceMs > 0) await twin.clock({ advanceMs });
    await new Promise((r) => setTimeout(r, stepMs));
    last = await fn();
  }
  if (pred(last)) return last;
  throw new Error(`pollUntil exhausted ${steps} steps; last=${JSON.stringify(last)}`);
}

// Advance a fixed number of reconcile ticks with no exit predicate — for "run N ticks, then
// assert the world did NOT change" scenarios (dry-run, disabled, capital-gate-while-poor).
export async function advanceTicks(steps: number, advanceMs: number, stepMs = 300): Promise<void> {
  for (let i = 0; i < steps; i++) {
    await twin.clock({ advanceMs });
    await new Promise((r) => setTimeout(r, stepMs));
  }
}
