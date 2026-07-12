import { runCli, TWIN_ADMIN } from './run-cli.js';

// ─── Daemon read-back loop ────────────────────────────────────────────────────────────
// The bootstrapper observes fleet state through the DAEMON, never by reading the twin
// directly: `ship refresh` forces the daemon to re-GET /my/ships/{s} from the twin and
// overwrite its LOCAL cache, printing the reconciled state; `ship show`/`ship list` then
// read that local cache back. So every behavioural assertion here drives the FULL chain —
// CLI action (async container) -> `ship refresh` (daemon re-sync from twin) -> the
// reconciled state reflects the effect. A wire-shape the client rejected would surface as a
// non-zero refresh exit or a state that never converges.

export interface ShipView {
  exitCode: number;
  stdout: string;
  stderr: string;
  symbol: string | null;
  status: string | null;      // Nav Status
  location: string | null;
  fuelCurrent: number | null;
  fuelCapacity: number | null;
}

/** Parse the shared `Ship Symbol/Location/Nav Status/Fuel` block printed by both
 *  `ship refresh` and `ship info` (identical format). */
export function parseShipView(res: { exitCode: number; stdout: string; stderr: string }): ShipView {
  const s = res.stdout;
  const grab = (re: RegExp): string | null => { const m = s.match(re); return m ? m[1] : null; };
  const fuel = s.match(/Fuel:\s+(\d+)\s*\/\s*(\d+)/);
  return {
    exitCode: res.exitCode,
    stdout: res.stdout,
    stderr: res.stderr,
    symbol: grab(/Ship Symbol:\s+(\S+)/),
    status: grab(/Nav Status:\s+(\S+)/),
    location: grab(/Location:\s+(\S+)/),
    fuelCurrent: fuel ? Number(fuel[1]) : null,
    fuelCapacity: fuel ? Number(fuel[2]) : null,
  };
}

/** Force a fresh daemon re-sync from the twin and return the reconciled ship view. */
export function refreshShip(ship: string): ShipView {
  return parseShipView(runCli(['ship', 'refresh', '--ship', ship, '--player-id', '1']));
}

/** Read the ship back through the daemon's LOCAL cache (`ship info` = GetShip, no forced
 *  re-sync) — proves the value `ship refresh` persisted is what `ship show` reflects. */
export function showShip(ship: string): ShipView {
  return parseShipView(runCli(['ship', 'info', '--ship', ship, '--player-id', '1']));
}

/** Poll `ship refresh` (daemon re-sync each tick) until `pred` holds or the bounded budget is
 *  spent. Arrivals/mutations are real-clock (compressed ~20x), so a few seconds is ample —
 *  we NEVER sleep for uncompressed travel. Returns the last view seen. */
export async function pollShip(
  ship: string,
  pred: (v: ShipView) => boolean,
  opts: { tries?: number; delayMs?: number } = {},
): Promise<ShipView> {
  const tries = opts.tries ?? 25;
  const delayMs = opts.delayMs ?? 400;
  let last = refreshShip(ship);
  for (let i = 0; i < tries; i++) {
    if (last.exitCode === 0 && pred(last)) return last;
    await new Promise((r) => setTimeout(r, delayMs));
    last = refreshShip(ship);
  }
  return last;
}

/** Cold reset: twin back to the registered agent + both starting hulls DOCKED at A1. */
export async function resetCold(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}',
  });
  if (res.status !== 200) throw new Error(`POST /_twin/reset -> ${res.status}`);
}

/** List the fleet as the daemon serves it (local cache). Returns the parsed JSON rows. */
export function listFleet(): Array<Record<string, unknown>> {
  const res = runCli(['ship', 'list', '--json', '--player-id', '1']);
  if (res.exitCode !== 0) throw new Error(`ship list failed (exit ${res.exitCode}): ${res.stderr}`);
  const out = res.stdout.trim();
  if (out === '' || out.startsWith('No ships found')) return [];
  return JSON.parse(out) as Array<Record<string, unknown>>;
}
