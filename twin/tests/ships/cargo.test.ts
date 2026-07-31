import { beforeAll, describe, expect, it } from 'vitest';
import { runCli, TWIN_ADMIN } from '../helpers/run-cli';

// ─────────────────────────────────────────────────────────────────────────────────────────────
// DECODE SMOKE ONLY — this is NOT a behaviour/economic proof.
//
// `ship buy`/`ship sell` build a real SpaceTradersClient pointed at the twin and call
// PurchaseCargo/SellCargo. All this file proves is that the twin's purchase/sell WIRE SHAPE decodes
// in the Go client: a non-zero exit or a decode error would mean the response shape drifted from
// what the client expects, so `exit 0 + the good echoed back in stdout` is a legitimate contract-
// DECODE signal — and nothing more. It says NOTHING about whether the trade actually moved cargo or
// credits; it would still pass if the twin silently no-op'd the purchase. Do not read it as evidence
// that buying/selling "works".
//
// >> The ECONOMIC EFFECT — buy fills the hold and debits credits, sell empties it and credits back,
//    the round-trip nets exactly the market spread, and over-sell is refused with the books intact —
//    is proven with before/after deltas read through the daemon in
//    tests/acceptance/cargo-trade.e2e.test.ts. Strengthen behaviour THERE, never here.
//
// Cold-start command frigate is DOCKED at A1, which sells SILICON_CRYSTALS (zero-nav trade).
// ─────────────────────────────────────────────────────────────────────────────────────────────

const SHIP = 'TWINAGENT-1';
const GOOD = 'SILICON_CRYSTALS'; // A1 market: purchasePrice 58

async function resetCold(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}',
  });
  expect(res.status, 'POST /_twin/reset').toBe(200);
}

describe('cargo buy/sell — WIRE-DECODE SMOKE (behaviour lives in cargo-trade.e2e)', () => {
  beforeAll(resetCold);

  it('`ship buy` decodes the purchase wire contract (exit 0 + good echoed) — decode only, not effect', () => {
    const { stdout, stderr, exitCode } = runCli(['ship', 'buy', '--ship', SHIP, '--good', GOOD, '--units', '5', '--player-id', '1']);
    expect(exitCode, `stderr: ${stderr}`).toBe(0);
    expect(stdout, 'the purchased good is echoed back — the response body decoded').toContain(GOOD);
  });

  it('`ship sell` decodes the sell wire contract (exit 0 + good echoed) — decode only, not effect', () => {
    const { stdout, stderr, exitCode } = runCli(['ship', 'sell', '--ship', SHIP, '--good', GOOD, '--units', '5', '--player-id', '1']);
    expect(exitCode, `stderr: ${stderr}`).toBe(0);
    expect(stdout, 'the sold good is echoed back — the response body decoded').toContain(GOOD);
  });
});
