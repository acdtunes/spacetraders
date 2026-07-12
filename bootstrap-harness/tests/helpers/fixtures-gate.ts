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
