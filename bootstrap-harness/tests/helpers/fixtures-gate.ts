// GATE-phase (post-INCOME) entry fixture for POST /_twin/reset { mode: "gate-entry" }.
export interface GateFixture {
  credits?: number;
  haulers?: number;             // idle income haulers available to repurpose
  incomePerHour?: number;       // >= income_bar so INCOME is complete
  gateSite?: string;            // the under-construction jump-gate waypoint
  gateMaterialChains?: number;  // producing chains (worker-sizing input)
  constructionPercent?: number; // starting % (default 0)
  workerPrice?: number;         // price to buy a top-up worker hull
  executorRunning?: boolean;    // is the construction executor already up
}

export function gateEntry(overrides: Partial<GateFixture> = {}): GateFixture {
  return {
    credits: 1_500_000,
    haulers: 4,
    incomePerHour: 50000,
    gateSite: 'X1-PZ28-I67', // the real era-2 JUMP_GATE waypoint (twin fixture waypoints.json); NOT I57
    gateMaterialChains: 3,
    constructionPercent: 0,
    workerPrice: 300000,
    executorRunning: true,
    ...overrides,
  };
}

// gateLowCredits is the GATE-entry fixture for the fleet-buy SOLVENCY spec (Option B): treasury sits low
// enough that buying a gate-delivery hull would drop it BELOW the shared working-capital floor
// (max(50k, 40%×treasury)), so the coordinator's solvency gate PAUSES the fleet buys.
//   floor(400k) = max(50k, 40%×400k=160k) = 160k; a 300k buy would leave 400k−300k = 100k < 160k ⇒ blocked.
// workerPrice matches the base shipyard SHIP_LIGHT_HAULER listing (300k, twin fixtures/era2-X1-PZ28/
// shipyards.json) so the daemon's price-check (the block decision) and the twin's per-PurchaseShip debit
// (routes/ships.ts) agree on the number. haulers>=1 so an idle contract hauler is available as the buy's
// purchaser once the treasury refills (they stay contract earners — never repurposed).
export function gateLowCredits(overrides: Partial<GateFixture> = {}): GateFixture {
  return gateEntry({
    credits: 400_000,
    workerPrice: 300_000,
    haulers: 4,
    ...overrides,
  });
}
