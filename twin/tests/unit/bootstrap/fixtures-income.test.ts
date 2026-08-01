import { describe, expect, it } from 'vitest';
import { incomeEntry } from '../../bootstrap/helpers/fixtures-income';

describe('incomeEntry fixture', () => {
  it('defaults to a post-DATA / INCOME-entry world', () => {
    expect(incomeEntry()).toEqual({
      credits: 600000, haulerPrice: 300000,
      hubs: ['X1-PZ28-H1', 'X1-PZ28-H2', 'X1-PZ28-H3', 'X1-PZ28-H4', 'X1-PZ28-H5'],
      frigateContractTagged: true, creditsPerHour: 0,
    });
  });
  it('applies overrides (shallow)', () => {
    const f = incomeEntry({ hubs: ['X1-PZ28-H1'], credits: 2_000_000 });
    expect(f.hubs).toEqual(['X1-PZ28-H1']);
    expect(f.credits).toBe(2_000_000);
    expect(f.haulerPrice).toBe(300000);
  });
});
