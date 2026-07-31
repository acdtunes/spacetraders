import { describe, expect, it } from 'vitest';
import { coldStart } from '../helpers/fixtures';
import { incomeEntry } from '../helpers/fixtures-income';
import { gateEntry } from '../helpers/fixtures-gate';

describe('coldStart (DATA) fixture', () => {
  it('defaults to the ~175k / 1 probe / 1 frigate cold start', () => {
    expect(coldStart()).toEqual({ credits: 175000, probes: 1, frigates: 1 });
  });
  it('applies overrides (shallow)', () => {
    expect(coldStart({ credits: 30000, probePrice: 40000 })).toEqual({
      credits: 30000, probes: 1, frigates: 1, probePrice: 40000,
    });
  });
});

describe('incomeEntry (INCOME) fixture', () => {
  it('defaults to a post-DATA world', () => {
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

describe('gateEntry (GATE) fixture', () => {
  it('defaults to a post-INCOME world', () => {
    expect(gateEntry()).toEqual({
      credits: 1_500_000, haulers: 4, incomePerHour: 50000,
      gateSite: 'X1-PZ28-I67', gateMaterialChains: 3, constructionPercent: 0,
      workerPrice: 300000, executorRunning: true,
    });
  });
  it('applies overrides (shallow)', () => {
    const f = gateEntry({ constructionPercent: 90, gateMaterialChains: 5 });
    expect(f.constructionPercent).toBe(90);
    expect(f.gateMaterialChains).toBe(5);
    expect(f.gateSite).toBe('X1-PZ28-I67');
  });
});
