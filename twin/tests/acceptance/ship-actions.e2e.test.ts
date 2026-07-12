import { beforeAll, describe, expect, it } from 'vitest';
import { runCli } from '../helpers/run-cli';
import { listFleet, pollShip, refreshShip, resetCold } from '../helpers/readback';

// ─────────────────────────────────────────────────────────────────────────────────────────────
// SHIP ACTIONS — behaviour acceptance (LIVE STACK: shared twin :8080 + test daemon).
//
// Sibling to cargo-trade.e2e.test.ts. Where that file proves the ECONOMIC effect of buy/sell, this
// one proves the PHYSICAL effects of a pilot commanding a hull: orbiting, docking, navigating,
// refuelling, and expanding the fleet at a shipyard.
//
// Every `ship orbit|dock|refuel|navigate` and `shipyard purchase` is ASYNCHRONOUS — the CLI only
// DISPATCHES a background daemon container and returns a Container ID immediately (see
// gobot/internal/adapters/cli/ship.go: OrbitShip/DockShip/RefuelShip/NavigateShip all print
// "operation started" + a container id). So the command's own stdout/exit proves NOTHING about the
// outcome. We therefore read the EFFECT back through the real observation path the bootstrapper
// uses: force the daemon to re-sync from the twin (`ship refresh`, via pollShip) and assert the
// reconciled Nav Status / Location / Fuel; and for the fleet, read the daemon's `ship list --json`
// roster (listFleet). Travel is real-clock but compressed ~20x, so a bounded poll of a few seconds
// suffices — we NEVER sleep for uncompressed travel.
//
// HAS-TEETH: each scenario captures a BEFORE observation and asserts the AFTER differs by the
// expected transition (status flip, location change off the origin, fuel drained→full, roster +1).
// A no-op endpoint — orbit that never leaves the dock, navigate that teleports nowhere, refuel that
// adds no fuel, purchase that adds no hull — makes an assertion FAIL. We do not lean on exitCode===0
// to prove behaviour (dispatch exit 0 is asserted only as a "the container actually started" sanity).
//
// FLIGHT-MODE (requirement 6): setting a ship's flight mode (CRUISE/BURN/DRIFT/STEALTH) is
// PATCH /my/ships/:s/nav on the twin and has NO low-level gobot CLI subcommand — verified: the only
// `FlightMode` reference under gobot/internal/adapters/cli is a read-only route-segment OUTPUT field
// (daemon_client.go:1802), not a setter. It is exercised in-process elsewhere, not through this CLI
// acceptance surface, and is left as a documented `it.skip` below rather than an invented command.
//
// Ground truth (twin/fixtures/era2-X1-PZ28):
//   • TWINAGENT-1 = COMMAND frigate, TWINAGENT-2 = SATELLITE/probe; both DOCKED at X1-PZ28-A1 on cold
//     start; player-id 1; agent holds 175,000 credits (register.json startingCredits).
//   • X1-PZ28-A1 (PLANET, marketplace) sells FUEL. X1-PZ28-F55 (PLANET) is the nearest waypoint that
//     is NOT a zero-distance orbital of A1 (d≈49.5; A2/A3/A4 sit on A1's exact coords) and its market
//     sells FUEL — so one short voyage both DRAINS the tank and lands the ship at a refuel point.
//   • X1-PZ28-A2 is a shipyard adjacent to A1 (same coords) selling SHIP_PROBE @ 24,680 — affordable
//     and a near-instant hop for the purchasing hull, so the purchase turns on the roster, not travel.
//
// DO NOT run alongside other agents: this drives the singleton live twin on :8080. The crafter
// verifies RED on the shared stack. Per-scenario RED expectations are at the foot of this file.
// ─────────────────────────────────────────────────────────────────────────────────────────────

const FRIGATE = 'TWINAGENT-1'; // COMMAND frigate — the hull the pilot manoeuvres
const PROBE_BUYER = 'TWINAGENT-2'; // SATELLITE/probe — untouched; runs the shipyard purchase
const HOME = 'X1-PZ28-A1'; // cold-start waypoint (marketplace sells FUEL)
const NEAR = 'X1-PZ28-F55'; // nearest non-orbital waypoint whose market sells FUEL (PLANET, d≈49.5)
const YARD = 'X1-PZ28-A2'; // shipyard adjacent to A1; sells SHIP_PROBE @ 24,680
const PROBE_TYPE = 'SHIP_PROBE';

const DOCKED = 'DOCKED';
const IN_ORBIT = 'IN_ORBIT';

/** Live agent credit balance via `player info` (straight from the twin agent API — same read-back the
 *  cargo-trade suite uses). Only a DIRECTIONAL check is made below, so no static price model is baked. */
function readCredits(): number {
  const res = runCli(['player', 'info', '--player-id', '1']);
  expect(res.exitCode, `player info failed: ${res.stderr}`).toBe(0);
  const m = res.stdout.match(/Credits:\s+(-?\d+)/);
  expect(m, `no "Credits:" line in player info stdout:\n${res.stdout}`).not.toBeNull();
  return Number(m![1]);
}

/** Bounded poll of the daemon's fleet roster (`ship list --json`) until `pred` holds or the budget is
 *  spent. Mirrors pollShip but for the whole roster — a newly purchased hull only appears once the
 *  purchase container has navigated, docked and bought. Returns the last roster seen. */
async function pollFleet(
  pred: (rows: Array<Record<string, unknown>>) => boolean,
  opts: { tries?: number; delayMs?: number } = {},
): Promise<Array<Record<string, unknown>>> {
  const tries = opts.tries ?? 40;
  const delayMs = opts.delayMs ?? 1000;
  let rows = listFleet();
  for (let i = 0; i < tries; i++) {
    if (pred(rows)) return rows;
    await new Promise((r) => setTimeout(r, delayMs));
    rows = listFleet();
  }
  return rows;
}

// ── Scenario 1: enter orbit ──────────────────────────────────────────────────────────────────────
describe('A ship leaves the dock and enters orbit (CLI → daemon read-back)', () => {
  // Given a freshly reset frontier: TWINAGENT-1 is DOCKED at X1-PZ28-A1.
  beforeAll(resetCold);

  it('puts TWINAGENT-1 into orbit — nav status flips DOCKED → IN_ORBIT without leaving the waypoint', async () => {
    // ── Given: the cold-start frigate is observed DOCKED at home ──
    const before = refreshShip(FRIGATE);
    expect(before.exitCode, `baseline refresh failed: ${before.stderr}`).toBe(0);
    expect(before.status, 'cold-start frigate must be DOCKED before it can orbit').toBe(DOCKED);
    expect(before.location, 'cold-start frigate is at A1').toBe(HOME);

    // ── When: the pilot takes the ship to orbit ──
    const cmd = runCli(['ship', 'orbit', '--ship', FRIGATE, '--player-id', '1']);
    expect(cmd.exitCode, `ship orbit dispatch failed: ${cmd.stderr}`).toBe(0);

    // ── Then: once the daemon re-syncs, the ship is observed IN_ORBIT (and has not moved) ──
    const after = await pollShip(FRIGATE, (v) => v.status === IN_ORBIT, { tries: 30, delayMs: 500 });
    expect(after.status, 'orbit must take effect and survive the daemon re-sync').toBe(IN_ORBIT);
    expect(after.location, 'orbiting does not change the waypoint').toBe(HOME);
  }, 60_000);
});

// ── Scenario 2: return and dock ──────────────────────────────────────────────────────────────────
describe('A ship returns from orbit and docks (CLI → daemon read-back)', () => {
  beforeAll(resetCold);

  it('docks TWINAGENT-1 — nav status flips IN_ORBIT → DOCKED at the same waypoint', async () => {
    // ── Given: put the frigate IN_ORBIT at home first ──
    expect(runCli(['ship', 'orbit', '--ship', FRIGATE, '--player-id', '1']).exitCode, 'arrange orbit dispatch').toBe(0);
    const inOrbit = await pollShip(FRIGATE, (v) => v.status === IN_ORBIT, { tries: 30, delayMs: 500 });
    expect(inOrbit.status, 'arrange: ship must be IN_ORBIT before docking').toBe(IN_ORBIT);

    // ── When: the pilot docks the ship ──
    const cmd = runCli(['ship', 'dock', '--ship', FRIGATE, '--player-id', '1']);
    expect(cmd.exitCode, `ship dock dispatch failed: ${cmd.stderr}`).toBe(0);

    // ── Then: after re-sync the ship is observed DOCKED (still at home) ──
    const after = await pollShip(FRIGATE, (v) => v.status === DOCKED, { tries: 30, delayMs: 500 });
    expect(after.status, 'dock must take effect and survive the daemon re-sync').toBe(DOCKED);
    expect(after.location, 'docking does not change the waypoint').toBe(HOME);
  }, 90_000);
});

// ── Scenario 3: navigate to another waypoint and arrive ──────────────────────────────────────────
describe('A ship navigates to another waypoint and arrives (CLI → daemon read-back)', () => {
  beforeAll(resetCold);

  it('navigates TWINAGENT-1 from A1 to F55 — its location CHANGES and it arrives in orbit', async () => {
    // ── Given: the frigate is DOCKED at its origin A1 ──
    const before = refreshShip(FRIGATE);
    expect(before.status, 'ship starts DOCKED at A1').toBe(DOCKED);
    expect(before.location, 'origin is A1').toBe(HOME);

    // ── When: the pilot sets course for the neighbouring waypoint ──
    const cmd = runCli(['ship', 'navigate', '--ship', FRIGATE, '--destination', NEAR, '--player-id', '1']);
    expect(cmd.exitCode, `ship navigate dispatch failed: ${cmd.stderr}`).toBe(0);

    // ── Then: bounded-poll the daemon (travel is real-clock, compressed ~20x) until the ship is
    //         observed AT the destination and IN_ORBIT. Teeth: it actually MOVED off its origin. ──
    const arrived = await pollShip(
      FRIGATE,
      (v) => v.location === NEAR && v.status === IN_ORBIT,
      { tries: 40, delayMs: 750 },
    );
    expect(arrived.location, 'ship must have travelled to the destination waypoint').toBe(NEAR);
    expect(arrived.location, 'teeth: the ship is no longer at its origin A1').not.toBe(HOME);
    expect(arrived.status, 'a completed voyage leaves the ship in orbit at the destination').toBe(IN_ORBIT);
  }, 90_000);
});

// ── Scenario 4: refuel a drained tank ────────────────────────────────────────────────────────────
describe('A pilot refuels a ship whose tank was drained by travel (CLI → daemon read-back)', () => {
  beforeAll(resetCold);

  it('refuels TWINAGENT-1 after a voyage — the tank is restored to full and credits are spent', async () => {
    // ── Given: drain the tank by navigating A1 → F55 (whose market sells FUEL), then dock there ──
    expect(runCli(['ship', 'navigate', '--ship', FRIGATE, '--destination', NEAR, '--player-id', '1']).exitCode, 'arrange navigate dispatch').toBe(0);
    await pollShip(FRIGATE, (v) => v.location === NEAR && v.status === IN_ORBIT, { tries: 40, delayMs: 750 });
    expect(runCli(['ship', 'dock', '--ship', FRIGATE, '--player-id', '1']).exitCode, 'arrange dock dispatch').toBe(0);
    // Poll dock on STATUS only: the ship is physically at F55 (the twin executed the voyage), and
    // location read-back is scenario 3's concern — keeping it out here isolates this scenario's RED
    // to the fuel/refuel gap rather than the navigation re-sync gap.
    const docked = await pollShip(FRIGATE, (v) => v.status === DOCKED, { tries: 30, delayMs: 500 });
    expect(docked.status, 'ship must be DOCKED at the fuel market to refuel').toBe(DOCKED);

    // capture the DRAINED tank before refuelling — this delta is what gives the refuel assertion teeth
    const fuelBefore = docked.fuelCurrent;
    const capacity = docked.fuelCapacity;
    expect(fuelBefore, 'fuel level must be observable').not.toBeNull();
    expect(capacity, 'fuel capacity must be observable').not.toBeNull();
    expect(fuelBefore as number, 'the voyage must have burned fuel — tank observed below capacity').toBeLessThan(capacity as number);
    const creditsBefore = readCredits();

    // ── When: the pilot refuels at the docked market ──
    const cmd = runCli(['ship', 'refuel', '--ship', FRIGATE, '--player-id', '1']);
    expect(cmd.exitCode, `ship refuel dispatch failed: ${cmd.stderr}`).toBe(0);

    // ── Then: the tank fills to capacity, strictly above the drained level ... ──
    const filled = await pollShip(
      FRIGATE,
      (v) => v.fuelCurrent !== null && v.fuelCapacity !== null && v.fuelCurrent === v.fuelCapacity,
      { tries: 30, delayMs: 500 },
    );
    expect(filled.fuelCurrent, 'refuel must fill the tank to capacity').toBe(filled.fuelCapacity);
    expect(filled.fuelCurrent as number, 'teeth: fuel strictly increased vs the drained level').toBeGreaterThan(fuelBefore as number);

    // ── ... and (secondary — credits are observable via `player info`) the fuel was paid for ──
    expect(readCredits(), 'refuelling is not free — the agent spent credits').toBeLessThan(creditsBefore);
  }, 150_000);
});

// ── Scenario 5: expand the fleet at a shipyard ───────────────────────────────────────────────────
describe('A captain expands the fleet by purchasing a new hull (CLI → daemon read-back)', () => {
  beforeAll(resetCold);

  it('purchases a SHIP_PROBE at the shipyard — the fleet roster grows by exactly one new hull', async () => {
    // ── Given: the cold-start roster (two hulls). Baseline captured as a delta, not a magic number. ──
    const before = listFleet();
    const beforeSymbols = before.map((r) => String(r.symbol));

    // ── When: buy a probe at adjacent shipyard A2 (sells SHIP_PROBE @ 24,680; agent holds 175,000).
    //         The purchase container navigates the (idle, adjacent) probe to the yard, docks, buys. ──
    const cmd = runCli(['shipyard', 'purchase', '--ship', PROBE_BUYER, '--type', PROBE_TYPE, '--waypoint', YARD, '--player-id', '1']);
    expect(cmd.exitCode, `shipyard purchase dispatch failed: ${cmd.stderr}`).toBe(0);

    // ── Then: bounded-poll the roster until a NEW hull appears. Teeth: count grows by exactly one. ──
    const after = await pollFleet((rows) => rows.length > before.length, { tries: 40, delayMs: 1000 });
    expect(after.length, 'the purchased hull must appear in the daemon fleet roster').toBe(before.length + 1);
    const newHulls = after.map((r) => String(r.symbol)).filter((s) => !beforeSymbols.includes(s));
    expect(newHulls.length, 'exactly one brand-new hull was added to the fleet').toBe(1);
  }, 150_000);
});

// ── Scenario 6: flight-mode — documented, intentionally uncovered here ────────────────────────────
describe('Flight-mode (CRUISE/BURN/DRIFT/STEALTH) — no low-level CLI to exercise', () => {
  // See the header note: there is no `ship set-flight-mode`/cruise/burn/drift verb in the gobot CLI
  // (the sole FlightMode reference under adapters/cli is the read-only route-segment output field at
  // daemon_client.go:1802). Setting flight mode is PATCH /my/ships/:s/nav on the twin and is covered
  // in-process elsewhere — NOT invented as a CLI command here.
  it.skip('sets a ship flight mode via CLI — intentionally uncovered: no low-level command exists', () => {});
});

// ─────────────────────────────────────────────────────────────────────────────────────────────
// WHY THIS IS RED NOW (candidate gaps for the crafter to confirm on the shared stack)
//
// The twin implements each action's world-state mutation; a RED here is expected to surface in the
// daemon READ-BACK / RE-SYNC chain — exactly the no-op classes the exit-code-only style cannot catch.
// Per scenario, the honest assertion is authored as the effect SHOULD read back; do NOT weaken it:
//
//   1. ORBIT / 2. DOCK — `ship orbit|dock` dispatch a container, then the daemon must re-sync the new
//      Nav Status on `ship refresh`. RED if the container's status change is not reflected by the
//      refresh path (pollShip exhausts its budget while status stays on the old value). These are the
//      likeliest to already pass (simple status flips); they are asserted as effects regardless.
//   3. NAVIGATE — the frigate physically travels A1→F55 on the twin, but the daemon must re-sync the
//      new Location (and IN_ORBIT arrival) for the roster to observe it. This is the brief's named
//      risk ("a nav mutation the daemon doesn't re-sync"): RED if refresh keeps reporting A1 /
//      IN_TRANSIT, so pollShip never sees location===F55 && IN_ORBIT.
//   4. REFUEL — teeth on FUEL: (a) the pre-refuel read must show the tank BELOW capacity (proving the
//      voyage's burn is observable through refresh) and (b) the post-refuel read must show it FULL and
//      strictly higher. RED if the daemon does not re-sync fuelCurrent (pre-check reads a stale full
//      tank, or the fill never surfaces). The credits check is secondary and shares the `player info`
//      balance read-back that the cargo-trade suite already flags as a candidate stale read.
//   5. PURCHASE — the new hull must appear in the daemon's `ship list` roster. This is the brief's
//      named risk ("purchase not reflected"): RED if the daemon never adds the purchased hull to its
//      fleet cache, so pollFleet exhausts its budget with the roster length unchanged.
// ─────────────────────────────────────────────────────────────────────────────────────────────
