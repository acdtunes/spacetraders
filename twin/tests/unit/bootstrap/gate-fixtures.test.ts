import { describe, expect, it } from 'vitest';
import { gateEntry } from '../../bootstrap/helpers/gate-fixtures';

describe('gateEntry fixture', () => {
  it('defaults to a post-INCOME / GATE-entry world', () => {
    expect(gateEntry()).toEqual({
      credits: 1_500_000, haulers: 4, incomePerHour: 50000,
      gateSite: 'X1-PZ28-I57', gateMaterialChains: 3, constructionPercent: 0,
      workerPrice: 300000, executorRunning: true,
    });
  });
  it('applies overrides (shallow)', () => {
    const f = gateEntry({ constructionPercent: 90, gateMaterialChains: 5 });
    expect(f.constructionPercent).toBe(90);
    expect(f.gateMaterialChains).toBe(5);
    expect(f.gateSite).toBe('X1-PZ28-I57');
  });
});
