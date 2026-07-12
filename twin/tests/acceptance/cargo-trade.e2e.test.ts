import { beforeAll, describe, expect, it } from 'vitest';
import { runCli } from '../helpers/run-cli';
import { listFleet, refreshShip, resetCold, type ShipView } from '../helpers/readback';

// ─────────────────────────────────────────────────────────────────────────────────────────────
// CARGO TRADE round-trip — behaviour acceptance (LIVE STACK: shared twin :8080 + test daemon).
//
// This is the NON-THEATRE counterpart to tests/ships/cargo.test.ts. That test proves only that the
// wire contract decodes (exit 0 + the good echoed in stdout) — it would still pass if the twin
// silently no-op'd the purchase. Here we prove the ECONOMIC EFFECT the user actually cares about:
// buying and its inverse selling are PAIRED, and the pairing conserves value up to the market spread.
//
// Every assertion reads the effect back through a REAL observation path, never the twin's /v2:
//   • CARGO  — after the direct-to-twin `ship buy`/`ship sell`, we force the daemon to re-sync with
//              `ship refresh` (GET /my/ships/<s> → overwrite local cache → print reconciled state),
//              then parse the daemon-reconciled `Cargo:` block. A second, independent aggregate check
//              reads `cargoUnits` off the daemon's `ship list --json` row.
//   • CREDITS — `player info` fetches the LIVE agent balance straight from the twin agent API.
//
// HAS-TEETH: we capture BEFORE values and assert the AFTER differs by the exact delta, on BOTH
// credits and cargo. A no-op buy, a no-op sell, a daemon that drops cargo on re-sync, or a stale
// credit read-back each make an assertion fail. We do NOT lean on exitCode===0 to prove behaviour.
//
// Ground truth (twin/fixtures/era2-X1-PZ28/markets.json @ X1-PZ28-A1; prices are static — the twin's
// cargo route never rewrites market prices, so deltas are exact):
//   SILICON_CRYSTALS  purchasePrice 58 (paid on BUY) / sellPrice 52 (received on SELL) → spread 6.
//   TWINAGENT-1 is a COMMAND frigate, cargo capacity 40, DOCKED at A1 on cold start; agent has 175,000
//   credits. tradeVolume 60 ≥ 10, so a 10-unit trade settles in a single transaction (no price walk).
//
// DO NOT run this alongside other agents: it drives the singleton live twin on :8080. The crafter
// verifies RED on the shared stack. See the RED expectations note at the foot of this file.
// ─────────────────────────────────────────────────────────────────────────────────────────────

const SHIP = 'TWINAGENT-1';
const GOOD = 'SILICON_CRYSTALS';
const PURCHASE_PRICE = 58; // credits paid per unit to BUY at A1
const SELL_PRICE = 52; // credits received per unit to SELL at A1
const SPREAD = PURCHASE_PRICE - SELL_PRICE; // 6 — the market's cut, lost on any instant round-trip
const UNITS = 10;

// ── Read-back parsers: observe EFFECTS through the real CLI surfaces (daemon + agent API) ─────────

/** Live agent credit balance, fetched by `player info` straight from the twin agent API. */
function readCredits(): number {
  const res = runCli(['player', 'info', '--player-id', '1']);
  expect(res.exitCode, `player info failed: ${res.stderr}`).toBe(0);
  const m = res.stdout.match(/Credits:\s+(-?\d+)/);
  expect(m, `no "Credits:" line in player info stdout:\n${res.stdout}`).not.toBeNull();
  return Number(m![1]);
}

/** Units of one good held, parsed from the daemon-reconciled `ship refresh` cargo-contents block
 *  ("  - <name>: <units> units (<SYMBOL>)"). Returns 0 when the good is absent. Keyed on the good
 *  appearing anywhere on a "<n> units" line, so it is robust to name/symbol column placement. */
function heldUnits(view: ShipView, good: string): number {
  const line = view.stdout
    .split('\n')
    .find((l) => l.includes(good) && /\d+\s+units/.test(l));
  if (!line) return 0;
  const m = line.match(/(\d+)\s+units/);
  return m ? Number(m[1]) : 0;
}

/** Total cargo units the daemon reports after re-sync ("Cargo:  <units> / <capacity> units"). */
function totalCargoUnits(view: ShipView): number {
  const m = view.stdout.match(/Cargo:\s+(\d+)\s*\/\s*\d+\s+units/);
  expect(m, `no "Cargo:" line in ship view stdout:\n${view.stdout}`).not.toBeNull();
  return Number(m![1]);
}

/** Independent aggregate cross-check: cargoUnits on the daemon's `ship list --json` row (local cache,
 *  read AFTER a refresh so it reflects what the re-sync persisted). */
function fleetCargoUnits(ship: string): number {
  const row = listFleet().find((r) => r.symbol === ship);
  expect(row, `ship ${ship} not present in daemon ship list`).toBeDefined();
  return Number(row!.cargoUnits);
}

// ── Scenario 1 (happy path / walking skeleton): the full buy↔sell round-trip ─────────────────────
describe('Cargo trade round-trip — a trader\'s credits and hold move together (CLI → daemon read-back)', () => {
  // Given a freshly reset frontier: TWINAGENT-1 (COMMAND frigate) is DOCKED at X1-PZ28-A1, whose
  // market trades SILICON_CRYSTALS (pay 58 / receive 52), and the agent holds 175,000 credits.
  beforeAll(resetCold);

  it('buys 10 silicon then sells it back — the hold fills and empties while credits net exactly the spread', () => {
    // ── Given: capture the BEFORE books and BEFORE hold, both read through the daemon/agent ──
    const before = refreshShip(SHIP);
    expect(before.exitCode, `baseline refresh failed: ${before.stderr}`).toBe(0);
    expect(before.status, 'ship must start DOCKED to trade at the market').toBe('DOCKED');
    const creditsBefore = readCredits();
    const siliconBefore = heldUnits(before, GOOD); // expected 0 at cold start — captured, not assumed
    const totalBefore = totalCargoUnits(before);

    // ── When: the trader BUYS 10 units of silicon at the docked market ──
    const buy = runCli(['ship', 'buy', '--ship', SHIP, '--good', GOOD, '--units', String(UNITS), '--player-id', '1']);
    expect(buy.exitCode, `ship buy failed: ${buy.stderr}`).toBe(0);

    // ── Then: after the daemon re-syncs, the hold HOLDS 10 more silicon and credits fell by 10*58 ──
    const afterBuy = refreshShip(SHIP);
    expect(afterBuy.exitCode, `refresh after buy failed: ${afterBuy.stderr}`).toBe(0);
    expect(heldUnits(afterBuy, GOOD), 'daemon-observed silicon held after buy').toBe(siliconBefore + UNITS);
    expect(totalCargoUnits(afterBuy), 'total hold after buy').toBe(totalBefore + UNITS);
    expect(fleetCargoUnits(SHIP), 'ship list --json cargoUnits after buy (independent aggregate)').toBe(totalBefore + UNITS);
    expect(readCredits(), 'credits after buy == before - 10*58').toBe(creditsBefore - UNITS * PURCHASE_PRICE);

    // ── When: the trader SELLS the same 10 units back into the market ──
    const sell = runCli(['ship', 'sell', '--ship', SHIP, '--good', GOOD, '--units', String(UNITS), '--player-id', '1']);
    expect(sell.exitCode, `ship sell failed: ${sell.stderr}`).toBe(0);

    // ── Then: the silicon is GONE, the hold is back to baseline, and the sell returned credits ──
    const afterSell = refreshShip(SHIP);
    expect(afterSell.exitCode, `refresh after sell failed: ${afterSell.stderr}`).toBe(0);
    expect(heldUnits(afterSell, GOOD), 'silicon held after sell (fully gone)').toBe(siliconBefore);
    expect(totalCargoUnits(afterSell), 'total hold back to baseline after sell').toBe(totalBefore);
    expect(fleetCargoUnits(SHIP), 'ship list --json cargoUnits after sell (independent aggregate)').toBe(totalBefore);

    const creditsAfter = readCredits();
    // the sell leg must actually return money — credits rose above the post-buy trough
    expect(creditsAfter, 'sell returned credits (rose vs the post-buy trough)').toBeGreaterThan(creditsBefore - UNITS * PURCHASE_PRICE);
    // ── HEADLINE: the round-trip is not free — net credit change == -(spread * units) exactly ──
    // (paid 10*58, received 10*52 → net -60). This is the buy↔sell PAIRING invariant; neither leg
    // alone can prove it, and any no-op on either leg makes this fail.
    expect(creditsAfter - creditsBefore, 'net round-trip credit delta == -(spread * units)').toBe(-(SPREAD * UNITS));
  });
});

// ── Scenario 2 (error path): overselling is refused and leaves the books untouched ───────────────
describe('Cargo trade guard — overselling is refused and the books stay intact (CLI → daemon read-back)', () => {
  beforeAll(resetCold);

  it('rejects selling more silicon than is held and leaves credits and hold unchanged', () => {
    // ── Given: the trader holds a real, non-empty position of exactly 5 silicon ──
    const buy = runCli(['ship', 'buy', '--ship', SHIP, '--good', GOOD, '--units', '5', '--player-id', '1']);
    expect(buy.exitCode, `setup buy failed: ${buy.stderr}`).toBe(0);
    const afterBuy = refreshShip(SHIP);
    expect(afterBuy.exitCode, `setup refresh failed: ${afterBuy.stderr}`).toBe(0);
    expect(heldUnits(afterBuy, GOOD), 'holds exactly 5 before the over-sell attempt').toBe(5);
    const creditsBefore = readCredits();
    const totalBefore = totalCargoUnits(afterBuy);

    // ── When: the trader tries to SELL 999 units — far more than the 5 on board ──
    const oversell = runCli(['ship', 'sell', '--ship', SHIP, '--good', GOOD, '--units', '999', '--player-id', '1']);

    // ── Then: the trade is REFUSED, and the CLI names WHY — insufficient cargo ──
    // The twin's over-sell guard returns business code 4218, but the CLI never reaches it: the sell
    // command's SellStrategy.ValidatePreconditions (gobot .../strategies/cargo_transaction_strategy.go)
    // short-circuits LOCALLY with `fmt.Errorf("insufficient cargo: need %d, have %d", ...)` BEFORE any
    // API call, so the numeric 4218 is not observable at this CLI boundary. We therefore pin the
    // deterministic insufficient-cargo message cobra prints to stderr (same pattern as
    // tests/endpoints/shipyard.errors) — it proves the refusal is for the RIGHT reason, not merely a
    // non-zero exit for any reason.
    expect(oversell.exitCode, 'overselling must be refused (non-zero exit)').not.toBe(0);
    expect(oversell.stderr, 'the refusal names its cause: insufficient cargo').toMatch(/insufficient cargo/i);

    // ── ... and NOTHING moved: the 5 units are still on board and credits are unchanged ──
    // (Teeth: a guard that errored yet partially sold would empty the hold AND raise credits — both
    //  of the following would catch it.)
    const after = refreshShip(SHIP);
    expect(after.exitCode, `refresh after refused sell failed: ${after.stderr}`).toBe(0);
    expect(heldUnits(after, GOOD), 'hold unchanged after refused over-sell').toBe(5);
    expect(totalCargoUnits(after), 'total hold unchanged after refused over-sell').toBe(totalBefore);
    expect(fleetCargoUnits(SHIP), 'ship list --json cargoUnits unchanged after refused over-sell').toBe(totalBefore);
    expect(readCredits(), 'credits unchanged after refused over-sell').toBe(creditsBefore);
  });
});

// ─────────────────────────────────────────────────────────────────────────────────────────────
// WHY THIS IS RED NOW (candidate gaps for the crafter to confirm on the shared stack):
//   The twin itself implements the economics correctly (twin/src/routes/cargo.ts: purchase adds
//   cargo + debits world.agent.credits; sell removes cargo + credits it back; the over-sell guard
//   returns 4218 BEFORE mutating). So a RED here is expected to surface in the OBSERVATION chain,
//   not the twin economics:
//     1. CARGO read-back via the daemon: `ship refresh`'s RefreshShip path must map the twin's
//        GET /my/ships cargo.inventory into the rendered "Cargo Contents" + cargoUnits. If the
//        daemon drops per-good inventory (or zeroes cargoUnits) on re-sync, the heldUnits/
//        fleetCargoUnits assertions fail — that is the FINDING flagged in the brief (the daemon may
//        need to sync cargo). The assertions are authored as the effect SHOULD read back.
//     2. CREDITS read-back via `player info`: if it reports the persisted DB balance instead of a
//        live agent fetch, the exact-delta credit assertions fail on a stale (unchanged) number.
//   These are exactly the no-op classes the theatre exemplar cannot detect. Do NOT weaken the
//   assertions to go green — the crafter closes the gap in production code until they pass.
// ─────────────────────────────────────────────────────────────────────────────────────────────
