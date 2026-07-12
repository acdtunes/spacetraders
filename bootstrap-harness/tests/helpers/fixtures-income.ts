// INCOME-phase (post-DATA) entry fixture for POST /_twin/reset { mode: "income-entry" }.
export interface IncomeFixture {
  credits?: number;
  haulerPrice?: number;
  hubs?: string[];
  frigateContractTagged?: boolean;
  creditsPerHour?: number;
}

export function incomeEntry(overrides: Partial<IncomeFixture> = {}): IncomeFixture {
  return {
    credits: 600000,
    haulerPrice: 300000,
    hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3', 'X1-PZ28-H4', 'X1-PZ28-H5'],
    frigateContractTagged: true,
    creditsPerHour: 0,
    ...overrides,
  };
}
