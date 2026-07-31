// INCOME-phase (post-DATA) entry fixture for POST /_twin/reset { mode: "income-entry" }.
export interface IncomeFixture {
  credits?: number;
  haulerPrice?: number;
  // Optional override of the twin's hub set. LEFT UNSET by default: the twin then derives world.hubs
  // from the REAL MARKETPLACE waypoints in its loaded topology — the same real waypoints the
  // coordinator ranks (selectContractHubs) and navigates haulers onto. Passing logical symbols here
  // (there are no H1..H5 on the real API) would make a placed hauler's real destination never match a
  // hub, so parkedHub would stay null. Only set this to constrain the valid-hub set to real waypoints.
  hubs?: string[];
  frigateContractTagged?: boolean;
  creditsPerHour?: number;
}

export function incomeEntry(overrides: Partial<IncomeFixture> = {}): IncomeFixture {
  return {
    credits: 600000,
    haulerPrice: 300000,
    frigateContractTagged: true,
    creditsPerHour: 0,
    ...overrides,
  };
}
