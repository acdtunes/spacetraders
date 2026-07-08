/**
 * Operational Pulse state machine (pure).
 *
 * Collapses three signals — backend connectivity, how long since the last
 * captain event, and whether any ship is mid-transit — into a single coarse
 * status the HUD (slice 5) renders:
 *
 *   'lost' — backend is unreachable; nothing else can be trusted.
 *   'live' — the fleet is demonstrably doing something (a ship is in transit,
 *            or an event landed within the idle window).
 *   'idle' — connected and quiet: no ship moving and no event for a while.
 */

export type OpsState = 'live' | 'idle' | 'lost';

/**
 * How long the fleet can go without a captain event AND with nothing in
 * transit before Operational Pulse reads 'idle' rather than 'live'. Chosen
 * comfortably above the polling backoff ceiling (60s) so a single slow cycle
 * never flips a busy fleet to idle.
 */
export const FLEET_IDLE_AFTER_MS = 90_000;

export interface OpsStateInput {
  /** Is the backend reachable (last poll cycle succeeded)? */
  connectionOk: boolean;
  /** Milliseconds since the most recent fleet event (Infinity if none yet). */
  lastEventAgeMs: number;
  /** Is at least one ship currently IN_TRANSIT? */
  anyShipInTransit: boolean;
}

export function deriveOpsState({
  connectionOk,
  lastEventAgeMs,
  anyShipInTransit,
}: OpsStateInput): OpsState {
  // Connectivity dominates: a dark backend is 'lost' regardless of the rest.
  if (!connectionOk) return 'lost';
  // Motion is the strongest liveness signal.
  if (anyShipInTransit) return 'live';
  // Connected + still: idle only once the quiet window has fully elapsed.
  if (lastEventAgeMs > FLEET_IDLE_AFTER_MS) return 'idle';
  return 'live';
}
