// DATA-phase (cold-start) fixture for POST /_twin/reset.
export interface ResetFixture {
  credits?: number;
  probes?: number;
  frigates?: number;
  probePrice?: number;
  preScoutedMarkets?: string[];
  coverage?: number;
}

export function coldStart(overrides: Partial<ResetFixture> = {}): ResetFixture {
  return { credits: 175000, probes: 1, frigates: 1, ...overrides };
}
