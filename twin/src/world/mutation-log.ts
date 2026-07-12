// twin/src/world/mutation-log.ts — the mutation-log primitive + the POST /_twin/report
// ingest core (applyReport). The mutation log is the twin's single append-only record of
// every state-changing event, feeding GET /_twin/state.mutationLog. Two kinds of entries:
//   • TWIN-OBSERVABLE (PurchaseShip, navigate): logged directly by the /v2 routes via
//     appendMutation as they process the request.
//   • DAEMON-INTERNAL (the six ops below): logged via applyReport when the coordinator POSTs
//     /_twin/report, which ALSO flips the op's paired flag exactly-once.
import type { MutationLogEntry, World } from './types.js';
import { getNow } from '../clock.js';

/** Append one record to world.mutationLog. `seq` is monotonic (1-indexed, one past the prior
 *  entry's seq — gap-free while the log is append-only); `at` is stamped from the WORLD clock
 *  (getNow), so it reflects advanceClock steps, not wall time. `detail` is dropped from the
 *  entry when undefined. Returns the pushed entry. */
export function appendMutation(world: World, call: string, detail?: Record<string, unknown>): MutationLogEntry {
  const last = world.mutationLog[world.mutationLog.length - 1];
  const entry: MutationLogEntry = {
    seq: (last ? last.seq : 0) + 1,
    call,
    at: getNow().toISOString(),
  };
  if (detail !== undefined) entry.detail = detail;
  world.mutationLog.push(entry);
  return entry;
}

/** A daemon->twin report: one of the six daemon-internal ops, delivered over POST /_twin/report
 *  (test-gated — only when the coordinator's API base is the twin; prod is unchanged). */
export interface ReportInput {
  call: string;
  detail?: Record<string, unknown>;
}

/** POST /_twin/report ingest core. Maps each recognized op to its paired flag and, IF the flag
 *  is not already in its post-op state, flips it and appends a mutation-log entry. The flag
 *  itself is the exactly-once guard, so a repeat report (duplicate heartbeat, daemon kill+reboot
 *  mid-run) is a pure no-op: no flip, no entry. Returns the appended entry on the first apply, or
 *  null when the report was a no-op (already applied, or an unrecognized call). The flip + append
 *  are a single synchronous unit, so the flag and its log entry never diverge.
 *
 *  Note executor-bounce guards on construction.adopted (NOT executorRunning): the gate seeds
 *  executorRunning:true, yet the bounce must still fire once — adopting the pipeline is what the
 *  flag records. */
export function applyReport(world: World, report: ReportInput): MutationLogEntry | null {
  switch (report.call) {
    case 'fleet-unassign':
      if (!world.frigateContractTagged) return null;
      world.frigateContractTagged = false;
      break;
    case 'batch-contract':
      if (world.batchContractRunning) return null;
      world.batchContractRunning = true;
      break;
    case 'construction-start':
      if (world.construction.started) return null;
      world.construction.started = true;
      break;
    case 'executor-bounce':
      if (world.construction.adopted) return null;
      world.construction.adopted = true;
      break;
    case 'launch-autosizer':
      if (world.autosizerRunning) return null;
      world.autosizerRunning = true;
      break;
    case 'launch-siting':
      if (world.standingCoordinators.siting) return null;
      world.standingCoordinators.siting = true;
      break;
    case 'launch-worker-rebalancer':
      if (world.standingCoordinators.workerRebalancer) return null;
      world.standingCoordinators.workerRebalancer = true;
      break;
    default:
      return null; // not one of the six exactly-once report ops — ignore
  }
  return appendMutation(world, report.call, report.detail);
}
