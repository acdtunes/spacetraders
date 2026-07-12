import { beforeAll, describe, expect, it } from 'vitest';
import { runCli, TWIN_ADMIN } from '../helpers/run-cli';

// CLI-acceptance for the cargo endpoints (POST /v2/my/ships/:s/purchase|sell) through the REAL Go
// client: `ship buy`/`ship sell` build a SpaceTradersClient pointed at the twin and call
// PurchaseCargo/SellCargo. A non-zero exit or a decode error would mean the twin's wire shape does
// not match what the client expects — so exit 0 + the transaction echoed in stdout IS the contract
// proof. The cold-start command frigate is DOCKED at A1, which sells SILICON_CRYSTALS (zero nav).

const SHIP = 'TWINAGENT-1';
const GOOD = 'SILICON_CRYSTALS'; // A1 market: purchasePrice 58

async function resetCold(): Promise<void> {
  const res = await fetch(`${TWIN_ADMIN}/reset`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}',
  });
  expect(res.status, 'POST /_twin/reset').toBe(200);
}

describe('`spacetraders ship buy` — POST /my/ships/:s/purchase via the real Go client', () => {
  beforeAll(resetCold);

  it('buys cargo at the docked market (exit 0 ⇒ purchase wire contract decodes)', () => {
    const { stdout, stderr, exitCode } = runCli(['ship', 'buy', '--ship', SHIP, '--good', GOOD, '--units', '5', '--player-id', '1']);
    expect(exitCode, `stderr: ${stderr}`).toBe(0);
    expect(stdout).toContain(GOOD);
  });

  it('sells cargo back into the market (exit 0 ⇒ sell wire contract decodes)', () => {
    const { stdout, stderr, exitCode } = runCli(['ship', 'sell', '--ship', SHIP, '--good', GOOD, '--units', '5', '--player-id', '1']);
    expect(exitCode, `stderr: ${stderr}`).toBe(0);
    expect(stdout).toContain(GOOD);
  });
});
