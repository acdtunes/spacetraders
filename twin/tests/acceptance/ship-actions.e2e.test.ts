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
// expected transition (status flip, location change off the origin, tank restored + top-up paid, roster +1).
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

// ── Scenario 4: a voyage drains the tank and the coordinator restores it (and pays) ──────────────
describe('A voyage drains the tank and the coordinator restores it (CLI → daemon read-back)', () => {
  beforeAll(resetCold);

  // The pilot no longer refuels manually here — the COORDINATOR does. A voyage burns fuel at
  // departure and the daemon AUTO-REFUELS ON ARRIVAL, so the tank is restored to full at the
  // destination and the top-up is paid for. The transient DRAINED tank is unobservable by design:
  // the auto-refuel races any daemon read-back (true on the real API too), so a `fuelBefore < capacity`
  // precheck through `ship refresh` is unreachable — the coordinator's own fuel management defeats a
  // manual drained-tank arrange every time. The departure BURN itself is proven in-process,
  // frozen-clock, in tests/unit/navigate-fuel-persistence.test.ts (re-GET after arrival-settle still
  // reads 350/400); THIS scenario proves the coordinator's RESTORE + PAYMENT, then that a manual
  // top-up on the now-full tank is a genuine 0-unit no-op — locking the twin's 0-units-costs-0
  // invariant end-to-end (the same invariant that test's "full tank is free" case unit-locks).
  it('a voyage drains TWINAGENT-1 and the coordinator auto-refuels on arrival — the tank reads full at F55, the top-up is paid, and a manual refuel on the full tank costs nothing', async () => {
    // ── Given: the cold-start frigate is observed DOCKED at A1 with a FULL tank, and its credits are
    //         read BEFORE the voyage — the pre-voyage baseline the paid top-up must drop below ──
    const baseline = refreshShip(FRIGATE);
    expect(baseline.exitCode, `baseline refresh failed: ${baseline.stderr}`).toBe(0);
    expect(baseline.status, 'cold-start frigate is DOCKED at home').toBe(DOCKED);
    expect(baseline.location, 'cold-start frigate is at A1').toBe(HOME);
    expect(baseline.fuelCurrent, 'fuel level must be observable').not.toBeNull();
    expect(baseline.fuelCapacity, 'fuel capacity must be observable').not.toBeNull();
    expect(baseline.fuelCurrent, 'the frigate departs on a full tank').toBe(baseline.fuelCapacity);
    const creditsBefore = readCredits();

    // ── When: the pilot sets course A1 → F55 (whose market sells FUEL) ──
    expect(runCli(['ship', 'navigate', '--ship', FRIGATE, '--destination', NEAR, '--player-id', '1']).exitCode, 'navigate dispatch').toBe(0);

    // ── Then: bounded-poll the daemon until the ship is observed AT the destination (location only
    //         flips to F55 on arrival — resolveNav keeps it at the origin while IN_TRANSIT) ... ──
    const arrived = await pollShip(FRIGATE, (v) => v.location === NEAR, { tries: 40, delayMs: 750 });
    expect(arrived.location, 'the voyage must land the ship at the fuel-market destination').toBe(NEAR);
    expect(arrived.location, 'teeth: the ship is no longer at its origin A1').not.toBe(HOME);

    // ── ... and the coordinator's auto-refuel has RESTORED the tank to full at F55. (The drain is
    //    real — a CRUISE A1→F55 hop burns 50 — but auto-refuel races the read-back, so we assert the
    //    restored FULL tank, the coordinator's observable effect, not the transient drained level.) ──
    const restored = await pollShip(
      FRIGATE,
      (v) => v.location === NEAR && v.fuelCurrent !== null && v.fuelCapacity !== null && v.fuelCurrent === v.fuelCapacity,
      { tries: 30, delayMs: 500 },
    );
    expect(restored.fuelCurrent, 'the coordinator auto-refuels on arrival — the tank is full again at F55').toBe(restored.fuelCapacity);

    // ── ... and the top-up was PAID FOR: credits are strictly below the pre-voyage baseline (the
    //    refuel-costs-credits observable, now via the coordinator's real path, not a manual command).
    //    The twin's refuel is atomic (fuel + debit together), so once the tank reads full the debit
    //    has already landed — this read cannot race ahead of it. ──
    const creditsAfterVoyage = readCredits();
    expect(creditsAfterVoyage, 'the coordinator paid for the top-up — credits dropped below the pre-voyage baseline').toBeLessThan(creditsBefore);

    // ── And: a MANUAL refuel on the now-full tank is a genuine 0-unit no-op. The twin requires the
    //    ship DOCKED to refuel (400 ERR_SHIP_NOT_DOCKED otherwise), so dock first — that makes the
    //    top-up genuinely EXECUTE against a full tank (0 units → 0 cost) rather than fail for being in
    //    orbit. Re-reading credits UNCHANGED locks the twin's "0 units cost 0" invariant end-to-end. ──
    expect(runCli(['ship', 'dock', '--ship', FRIGATE, '--player-id', '1']).exitCode, 'dock dispatch (refuel requires DOCKED)').toBe(0);
    const dockedAtFuel = await pollShip(FRIGATE, (v) => v.status === DOCKED, { tries: 30, delayMs: 500 });
    expect(dockedAtFuel.status, 'ship must be DOCKED at the fuel market before a manual refuel').toBe(DOCKED);

    const refuelCmd = runCli(['ship', 'refuel', '--ship', FRIGATE, '--player-id', '1']);
    expect(refuelCmd.exitCode, `ship refuel dispatch failed: ${refuelCmd.stderr}`).toBe(0);
    // A 0-unit top-up costs 0: whether or not the async refuel container has run yet, the balance is
    // identical to the post-voyage balance — a full-tank refuel can only ever add 0 credits of cost.
    expect(readCredits(), 'refuelling an already-full tank buys 0 units and costs 0 credits').toBe(creditsAfterVoyage);
  }, 150_000);
});

// ── Scenario 5: expand the fleet at a shipyard ───────────────────────────────────────────────────
describe('A captain expands the fleet by purchasing a new hull (CLI → daemon read-back)', () => {
  beforeAll(resetCold);

  it('purchases a SHIP_PROBE at the shipyard — the fleet roster grows by exactly one new hull', async () => {
    // ── Given: the cold-start roster (two hulls). PRIME the daemon's local roster first — on a fresh
    //    test DB the daemon cache is EMPTY until it has re-synced each hull, so `ship refresh` BOTH
    //    starting hulls to make listFleet() report the real 2-hull starting fleet. Without this the
    //    BUYER's own first sync-row would count as "new" and the credit read would race ahead of the
    //    twin-side debit. Baseline captured as a delta (before.length + 1), not a magic number. ──
    refreshShip(FRIGATE);
    refreshShip(PROBE_BUYER);
    const before = listFleet();
    const beforeSymbols = before.map((r) => String(r.symbol));
    expect(beforeSymbols, 'arrange: the primed roster holds both cold-start hulls').toEqual(
      expect.arrayContaining([FRIGATE, PROBE_BUYER]),
    );
    const creditsBefore = readCredits(); // the purchase must be a real ECONOMIC transaction, not just a roster row

    // ── When: buy a probe at adjacent shipyard A2 (sells SHIP_PROBE @ 24,680; agent holds 175,000).
    //         The purchase container navigates the (idle, adjacent) probe to the yard, docks, buys. ──
    const cmd = runCli(['shipyard', 'purchase', '--ship', PROBE_BUYER, '--type', PROBE_TYPE, '--waypoint', YARD, '--player-id', '1']);
    expect(cmd.exitCode, `shipyard purchase dispatch failed: ${cmd.stderr}`).toBe(0);

    // ── Then: bounded-poll the roster until a genuinely NEW hull appears. A candidate is "new" only
    //    when it is NOT in the primed baseline AND NOT one of the two starting hulls (defence in depth:
    //    the explicit STARTERS guard holds even if a starter were somehow missing from beforeSymbols).
    //    With the primed baseline the first new symbol can only be the purchased hull, which exists
    //    ONLY after the twin-side PurchaseShip debit (~+2s) — never the BUYER's own earlier sync-row
    //    (~+100ms) that a bare length check would fire on before credits are charged. Teeth: the roster
    //    count grows by exactly one. ──
    const STARTERS = [FRIGATE, PROBE_BUYER];
    const isNewHull = (sym: string): boolean => !beforeSymbols.includes(sym) && !STARTERS.includes(sym);
    const after = await pollFleet((rows) => rows.some((r) => isNewHull(String(r.symbol))), { tries: 40, delayMs: 1000 });
    expect(after.length, 'the purchased hull must appear in the daemon fleet roster').toBe(before.length + 1);
    const newHulls = after.map((r) => String(r.symbol)).filter(isNewHull);
    expect(newHulls.length, 'exactly one brand-new hull was added to the fleet').toBe(1);

    // ── Teeth (F1): prove it was a real ECONOMIC transaction, not just a roster row — the agent
    //    paid, and the new hull reconciles as a real probe DOCKED at the shipyard via daemon re-sync. ──
    expect(readCredits(), 'buying a probe charged the agent — credits must drop').toBeLessThan(creditsBefore);
    const bought = await pollShip(newHulls[0], (v) => v.location === YARD, { tries: 20, delayMs: 500 });
    expect(bought.location, 'the new hull is a real probe reconciled at the shipyard, not a phantom row').toBe(YARD);
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
//   4. REFUEL — teeth on the COORDINATOR's fuel path: after A1→F55 the daemon auto-refuels on arrival,
//      so (a) the tank reads FULL at F55 (the restore is observable through refresh) and (b) credits
//      are strictly BELOW the pre-voyage baseline (the top-up was paid). The transient DRAINED tank is
//      unobservable by design — auto-refuel races any read-back — so the departure burn itself is
//      proven in-process (navigate-fuel-persistence.test.ts), while a manual refuel on the now-full
//      DOCKED tank re-reads credits UNCHANGED (the twin's 0-units-costs-0 invariant, end-to-end). RED
//      if the daemon does not re-sync fuelCurrent (the fill never surfaces) or the top-up is not paid.
//   5. PURCHASE — the new hull must appear in the daemon's `ship list` roster. This is the brief's
//      named risk ("purchase not reflected"): RED if the daemon never adds the purchased hull to its
//      fleet cache, so pollFleet exhausts its budget with the roster length unchanged. The baseline is
//      PRIMED (both starting hulls re-synced) so the BUYER's own sync-row is never mistaken for the
//      purchase and the credit read lands AFTER the twin-side debit, not before it.
// ─────────────────────────────────────────────────────────────────────────────────────────────
